// images_proxy.go —— 图片返回防盗链代理。
//
// 背景:chatgpt.com 返回给我们的图片下载 URL 有两种:
//  1. file-service 直出:https://files.oaiusercontent.com/...(签名直链,15 分钟有效,浏览器可直接访问)
//  2. sediment / estuary:https://chatgpt.com/backend-api/estuary/content?...
//     这种 URL **必须带 Authorization: Bearer <AT>** 才能下载,
//     直接把它塞进 <img src> 返回给前端,浏览器 100% 403。
//
// 方案:后端不再把上游 URL 直接暴露给客户端,改成生成一个自家签名 URL:
//
//	GET /p/img/<task_id>/<idx>?exp=<unix_ms>&sig=<hex>
//
// 请求到达时,后端:
//  1. 校验 exp 未过期 + sig 匹配(HMAC-SHA256,进程级随机 secret);
//  2. 用 DAO 按 task_id 查任务,找到 file_ids[idx] / account_id / conversation_id;
//  3. 用账号 AT + deviceID + proxy 构造一个 chatgpt.Client;
//  4. 调 ImageDownloadURL 拿当前有效的上游签名 URL;
//  5. 调 FetchImage 把字节拉下来,按 Content-Type 原样写回给浏览器。
//
// 这样前端只看见自家 host 的 URL,不再受防盗链 / Authorization 困扰;
// 上游签名 URL 过期也不怕,每次访问都现取。
package gateway

import (
	"context"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/432539/gpt2api/internal/image"
	"github.com/432539/gpt2api/internal/upstream/chatgpt"
	"github.com/432539/gpt2api/pkg/logger"
)

// ImageAccountResolver 按账号 ID 解出构造 chatgpt client 所需的敏感字段。
// 由 main.go 注入。接口里不直接依赖 account 包,保持本层解耦。
type ImageAccountResolver interface {
	AuthToken(ctx context.Context, accountID uint64) (at, deviceID, cookies string, err error)
	ProxyURL(ctx context.Context, accountID uint64) string
}

// ImageProxy 按签名代理下载上游图片。无需 API Key,只靠 URL 签名校验。
func (h *ImagesHandler) ImageProxy(c *gin.Context) {
	taskID := c.Param("task_id")
	idxStr := c.Param("idx")
	expStr := c.Query("exp")
	sig := c.Query("sig")

	if taskID == "" || idxStr == "" || expStr == "" || sig == "" {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	idx, err := strconv.Atoi(idxStr)
	if err != nil || idx < 0 || idx > 64 {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	expMs, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	if !image.VerifyImageProxyURL(taskID, idx, expMs, sig) {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}
	if h.DAO == nil {
		c.AbortWithStatus(http.StatusServiceUnavailable)
		return
	}

	t, err := h.DAO.Get(c.Request.Context(), taskID)
	if err != nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	outputs, _ := h.DAO.ListOutputs(c.Request.Context(), taskID)
	if idx < len(outputs) {
		out := outputs[idx]
		switch out.SourceType {
		case "stored_blob":
			body, err := os.ReadFile(out.SourceRef)
			if err != nil {
				c.AbortWithStatus(http.StatusBadGateway)
				return
			}
			ct := out.ContentType
			if ct == "" {
				ct = "image/png"
			}
			c.Header("Cache-Control", "private, max-age=1800")
			c.Data(http.StatusOK, ct, body)
			return
		case "remote_url":
			http.Redirect(c.Writer, c.Request, out.SourceRef, http.StatusTemporaryRedirect)
			return
		case "chatgpt_ref":
			// 继续走 legacy reverse 下载链路。
		default:
			// 回退到 legacy 逻辑。
		}
	}
	fids := t.DecodeFileIDs()
	if idx >= len(fids) {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	ref := fids[idx] // 可能是 "sed:xxxx" 或 "xxxx"
	if t.AccountID == 0 || h.ImageAccResolver == nil {
		c.AbortWithStatus(http.StatusServiceUnavailable)
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 60*time.Second)
	defer cancel()

	at, deviceID, cookies, err := h.ImageAccResolver.AuthToken(ctx, t.AccountID)
	if err != nil {
		logger.L().Warn("image proxy resolve account",
			zap.Error(err), zap.Uint64("account_id", t.AccountID))
		c.AbortWithStatus(http.StatusBadGateway)
		return
	}
	proxyURL := h.ImageAccResolver.ProxyURL(ctx, t.AccountID)

	cli, err := chatgpt.New(chatgpt.Options{
		AuthToken: at,
		DeviceID:  deviceID,
		ProxyURL:  proxyURL,
		Cookies:   cookies,
		Timeout:   h.upstreamTimeout(),
	})
	if err != nil {
		logger.L().Warn("image proxy build client", zap.Error(err))
		c.AbortWithStatus(http.StatusBadGateway)
		return
	}

	signedURL, err := cli.ImageDownloadURL(ctx, t.ConversationID, ref)
	if err != nil {
		logger.L().Warn("image proxy download_url",
			zap.Error(err), zap.String("task_id", taskID), zap.String("ref", ref))
		c.AbortWithStatus(http.StatusBadGateway)
		return
	}

	body, ct, err := cli.FetchImage(ctx, signedURL, 16*1024*1024)
	if err != nil {
		logger.L().Warn("image proxy fetch",
			zap.Error(err), zap.String("task_id", taskID))
		c.AbortWithStatus(http.StatusBadGateway)
		return
	}
	if ct == "" {
		ct = "image/png"
	}
	c.Header("Cache-Control", "private, max-age=1800")
	c.Data(http.StatusOK, ct, body)
}
