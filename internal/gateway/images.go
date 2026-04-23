package gateway

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/432539/gpt2api/internal/apikey"
	"github.com/432539/gpt2api/internal/billing"
	"github.com/432539/gpt2api/internal/image"
	modelpkg "github.com/432539/gpt2api/internal/model"
	"github.com/432539/gpt2api/internal/upstream/chatgpt"
	"github.com/432539/gpt2api/internal/usage"
	"github.com/432539/gpt2api/pkg/logger"
)

// 单张参考图的硬上限(字节)。chatgpt.com 的 /backend-api/files 实测上限大致 20MB。
const maxReferenceImageBytes = 20 * 1024 * 1024

// 同一次请求最多携带的参考图数量。
const maxReferenceImages = 4

// chatMsg 是 OpenAI chat message 的本地别名,便于 handleChatAsImage 内部表达。
type chatMsg = chatgpt.ChatMessage

// ImagesHandler 挂载在 /v1/images/* 下的处理器。
//
// 复用 Handler 的依赖(鉴权/模型/计费/限流/usage)加上专属的 image.Runner 和 DAO。
// 路由:
//
//	POST /v1/images/generations       同步生图(默认)
//	GET  /v1/images/tasks/:id         查询历史任务(按 task_id)
type ImagesHandler struct {
	*Handler
	Runner *image.Runner
	DAO    *image.DAO
	// ImageAccResolver 可选:代理下载上游图片时用于解出账号 AT/cookies/proxy。
	// 未注入时 /p/img 路径会返回 503。
	ImageAccResolver ImageAccountResolver
}

// ImageGenRequest OpenAI 兼容入参。
//
// 对 reference_images 的扩展:OpenAI 的 /images/generations 规范没有这个字段;
// 这里加一项非标准扩展,便于 Playground / Web UI 发起"图生图"走同一条 generations 路径。
// 每一项可以是:
//   - https:// URL       直接 HTTP GET
//   - data:<mime>;base64,xxxx   dataURL
//   - 纯 base64 字符串            兼容
type ImageGenRequest struct {
	Model             string   `json:"model"`
	Prompt            string   `json:"prompt"`
	N                 int      `json:"n"`
	Size              string   `json:"size"`
	Quality           string   `json:"quality,omitempty"`
	Style             string   `json:"style,omitempty"`
	Background        string   `json:"background,omitempty"`
	OutputFormat      string   `json:"output_format,omitempty"`
	OutputCompression int      `json:"output_compression,omitempty"`
	Moderation        string   `json:"moderation,omitempty"`
	ResponseFormat    string   `json:"response_format,omitempty"` // url | b64_json
	RoutePolicy       string   `json:"route_policy,omitempty"`    // auto | safe | responses
	User              string   `json:"user,omitempty"`
	ReferenceImages   []string `json:"reference_images,omitempty"` // 非标准扩展,见注释
}

// ImageGenData 单张图响应。
type ImageGenData struct {
	URL           string `json:"url,omitempty"`
	B64JSON       string `json:"b64_json,omitempty"`
	RevisedPrompt string `json:"revised_prompt,omitempty"`
	FileID        string `json:"file_id,omitempty"` // chatgpt.com 侧原始 id(用于对账)
}

// ImageGenResponse OpenAI 兼容返回。
type ImageGenResponse struct {
	Created int64          `json:"created"`
	Data    []ImageGenData `json:"data"`
	TaskID  string         `json:"task_id,omitempty"`
	// IsPreview=true 表示本次账号未命中 IMG2 灰度,返回的是 IMG1 预览图。
	// 前端可据此给用户一个"本次未使用 IMG2 生成"之类的软提示。
	IsPreview bool `json:"is_preview,omitempty"`
}

// ImageGenerations POST /v1/images/generations。
func (h *ImagesHandler) ImageGenerations(c *gin.Context) {
	startAt := time.Now()
	ak, ok := apikey.FromCtx(c)
	if !ok {
		openAIError(c, http.StatusUnauthorized, "missing_api_key", "缺少 API Key")
		return
	}

	var req ImageGenRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		openAIError(c, http.StatusBadRequest, "invalid_request_error", "请求参数错误:"+err.Error())
		return
	}
	if strings.TrimSpace(req.Prompt) == "" {
		openAIError(c, http.StatusBadRequest, "invalid_request_error", "prompt 不能为空")
		return
	}
	if req.Model == "" {
		req.Model = "gpt-image-2"
	}
	if req.N <= 0 {
		req.N = 1
	}
	if req.N > 4 {
		req.N = 4
	}
	if req.Size == "" {
		req.Size = "1024x1024"
	}

	refs, err := decodeReferenceInputs(c.Request.Context(), req.ReferenceImages)
	if err != nil {
		openAIError(c, http.StatusBadRequest, "invalid_reference_image", "参考图解析失败:"+err.Error())
		return
	}
	canonical := image.CanonicalRequest{
		Operation:         image.OperationGenerate,
		Model:             req.Model,
		Prompt:            maybeAppendClaritySuffix(req.Prompt),
		N:                 req.N,
		Size:              req.Size,
		ResponseFormat:    req.ResponseFormat,
		RoutePolicy:       coalesceRoutePolicy(forcedImageRoute(c), req.RoutePolicy),
		User:              req.User,
		ReferenceImages:   toInputImages(refs),
		Quality:           req.Quality,
		Style:             req.Style,
		Background:        req.Background,
		OutputFormat:      req.OutputFormat,
		OutputCompression: req.OutputCompression,
		Moderation:        req.Moderation,
	}
	canonical.Normalize()

	m, ratio, rpmCap, rec, refID, fail, ok := h.prepareImageRequest(c, ak, req.Model)
	if !ok {
		return
	}
	defer func() {
		rec.DurationMs = int(time.Since(startAt).Milliseconds())
		if rec.Status == "" {
			rec.Status = usage.StatusFailed
		}
		if h.Usage != nil {
			h.Usage.Write(rec)
		}
	}()
	rec.ModelID = m.ID
	if h.Limiter != nil {
		if ok, _, err := h.Limiter.AllowRPM(c.Request.Context(), ak.ID, rpmCap); err == nil && !ok {
			fail("rate_limit_rpm")
			openAIError(c, http.StatusTooManyRequests, "rate_limit_rpm", "触发每分钟请求数限制 (RPM),请稍后再试")
			return
		}
	}

	cost := billing.ComputeImageCost(m, canonical.N, ratio)
	refunded := false
	refund := func(code string) {
		fail(code)
		if refunded || cost == 0 {
			return
		}
		refunded = true
		_ = h.Billing.Refund(context.Background(), ak.UserID, ak.ID, cost, refID, "image refund")
	}
	if cost > 0 {
		if err := h.Billing.PreDeduct(c.Request.Context(), ak.UserID, ak.ID, cost, refID, "image prepay"); err != nil {
			if errors.Is(err, billing.ErrInsufficient) {
				fail("insufficient_balance")
				openAIError(c, http.StatusPaymentRequired, "insufficient_balance", "积分不足,请前往「账单与充值」充值后再试")
				return
			}
			fail("billing_error")
			openAIError(c, http.StatusInternalServerError, "billing_error", "计费异常:"+err.Error())
			return
		}
	}

	taskID := image.GenerateTaskID()
	if h.DAO != nil {
		if err := h.DAO.Create(c.Request.Context(), &image.Task{
			TaskID:             taskID,
			UserID:             ak.UserID,
			KeyID:              ak.ID,
			ModelID:            m.ID,
			Prompt:             canonical.Prompt,
			N:                  canonical.N,
			Size:               canonical.Size,
			Operation:          canonical.Operation,
			RoutePolicy:        canonical.RoutePolicy,
			RequestOptionsJSON: canonical.RequestOptionsJSON(),
			Status:             image.StatusDispatched,
			EstimatedCredit:    cost,
		}); err != nil {
			refund("billing_error")
			openAIError(c, http.StatusInternalServerError, "internal_error", "创建任务失败:"+err.Error())
			return
		}
	}

	runCtx, cancel := context.WithTimeout(c.Request.Context(), 8*time.Minute)
	defer cancel()
	res := h.Runner.Run(runCtx, image.RunOptions{
		TaskID:        taskID,
		UserID:        ak.UserID,
		KeyID:         ak.ID,
		ModelID:       m.ID,
		UpstreamModel: m.UpstreamModelSlug,
		Request:       canonical,
		MaxAttempts:   2,
	})
	rec.AccountID = res.AccountID
	if res.Status != image.StatusSuccess {
		refund(ifEmpty(res.ErrorCode, "upstream_error"))
		httpStatus := http.StatusBadGateway
		if res.ErrorCode == image.ErrNoAccount || res.ErrorCode == image.ErrRateLimited {
			httpStatus = http.StatusServiceUnavailable
		}
		openAIError(c, httpStatus, ifEmpty(res.ErrorCode, "upstream_error"), localizeImageErr(res.ErrorCode, res.ErrorMessage))
		return
	}

	if cost > 0 {
		if err := h.Billing.Settle(context.Background(), ak.UserID, ak.ID, cost, cost, refID, "image settle"); err != nil {
			logger.L().Error("billing settle image", zap.Error(err), zap.String("ref", refID))
		}
	}
	_ = h.Keys.DAO().TouchUsage(context.Background(), ak.ID, c.ClientIP(), cost)
	rec.Status = usage.StatusSuccess
	rec.CreditCost = cost
	if h.DAO != nil {
		_ = h.DAO.UpdateCost(c.Request.Context(), taskID, cost)
	}
	c.JSON(http.StatusOK, buildImageResponse(taskID, res, canonical.ResponseFormat))
}

// ImageTask GET /v1/images/tasks/:id。
func (h *ImagesHandler) ImageTask(c *gin.Context) {
	ak, ok := apikey.FromCtx(c)
	if !ok {
		openAIError(c, http.StatusUnauthorized, "missing_api_key", "缺少 API Key")
		return
	}
	id := c.Param("id")
	if id == "" {
		openAIError(c, http.StatusBadRequest, "invalid_request_error", "task id 不能为空")
		return
	}
	if h.DAO == nil {
		openAIError(c, http.StatusInternalServerError, "not_configured", "图片任务存储未初始化,请联系管理员")
		return
	}
	t, err := h.DAO.Get(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, image.ErrNotFound) {
			openAIError(c, http.StatusNotFound, "not_found", "任务不存在")
			return
		}
		openAIError(c, http.StatusInternalServerError, "internal_error", "查询任务失败:"+err.Error())
		return
	}
	if t.UserID != ak.UserID {
		openAIError(c, http.StatusNotFound, "not_found", "任务不存在")
		return
	}

	outputs, _ := h.DAO.ListOutputs(c.Request.Context(), t.TaskID)
	data := make([]ImageGenData, 0)
	if len(outputs) > 0 {
		data = make([]ImageGenData, 0, len(outputs))
		for _, out := range outputs {
			data = append(data, ImageGenData{
				URL:           image.BuildImageProxyURL(t.TaskID, out.OutputIndex, image.ImageProxyTTL),
				RevisedPrompt: out.RevisedPrompt,
			})
		}
	} else {
		urls := t.DecodeResultURLs()
		fileIDs := t.DecodeFileIDs()
		data = make([]ImageGenData, 0, len(urls))
		for i := range urls {
			d := ImageGenData{URL: image.BuildImageProxyURL(t.TaskID, i, image.ImageProxyTTL)}
			if i < len(fileIDs) {
				d.FileID = strings.TrimPrefix(fileIDs[i], "sed:")
			}
			data = append(data, d)
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"task_id":         t.TaskID,
		"status":          t.Status,
		"conversation_id": t.ConversationID,
		"created":         t.CreatedAt.Unix(),
		"finished_at":     nullableUnix(t.FinishedAt),
		"error":           t.Error,
		"credit_cost":     t.CreditCost,
		"data":            data,
	})
}

// handleChatAsImage 是 /v1/chat/completions 发现 model.type=image 时的转派点。
// 行为:
//   - 取最后一条 user message 作为 prompt
//   - 走完整图像链路(同 /v1/images/generations)
//   - 以 assistant message(含 markdown 图片链接)的 OpenAI chat 响应返回
//
// 这样前端只要调用一个端点 /v1/chat/completions,切换 model=gpt-image-2 就能出图。
func (h *ImagesHandler) handleChatAsImage(c *gin.Context, rec *usage.Log, ak *apikey.APIKey,
	m *modelpkg.Model, req *ChatCompletionsRequest, startAt time.Time) {
	rec.ModelID = m.ID
	rec.Type = usage.TypeImage

	prompt := extractLastUserPrompt(req.Messages)
	if strings.TrimSpace(prompt) == "" {
		rec.Status = usage.StatusFailed
		rec.ErrorCode = "invalid_request_error"
		openAIError(c, http.StatusBadRequest, "invalid_request_error",
			"图像模型需要用户消息作为 prompt,请检查 messages 内容")
		return
	}

	refID := uuid.NewString()

	// 倍率 + RPM
	ratio := 1.0
	rpmCap := ak.RPM
	if h.Groups != nil {
		if g, err := h.Groups.OfUser(c.Request.Context(), ak.UserID); err == nil && g != nil {
			ratio = g.Ratio
			if rpmCap == 0 {
				rpmCap = g.RPMLimit
			}
		}
	}
	if h.Limiter != nil {
		if ok, _, err := h.Limiter.AllowRPM(c.Request.Context(), ak.ID, rpmCap); err == nil && !ok {
			rec.Status = usage.StatusFailed
			rec.ErrorCode = "rate_limit_rpm"
			openAIError(c, http.StatusTooManyRequests, "rate_limit_rpm",
				"触发每分钟请求数限制 (RPM),请稍后再试")
			return
		}
	}

	// 预扣
	cost := billing.ComputeImageCost(m, 1, ratio)
	if cost > 0 {
		if err := h.Billing.PreDeduct(c.Request.Context(), ak.UserID, ak.ID, cost, refID, "chat->image prepay"); err != nil {
			rec.Status = usage.StatusFailed
			if errors.Is(err, billing.ErrInsufficient) {
				rec.ErrorCode = "insufficient_balance"
				openAIError(c, http.StatusPaymentRequired, "insufficient_balance",
					"积分不足,请前往「账单与充值」充值后再试")
				return
			}
			rec.ErrorCode = "billing_error"
			openAIError(c, http.StatusInternalServerError, "billing_error", "计费异常:"+err.Error())
			return
		}
	}
	refunded := false
	refund := func(code string) {
		rec.Status = usage.StatusFailed
		rec.ErrorCode = code
		if refunded || cost == 0 {
			return
		}
		refunded = true
		_ = h.Billing.Refund(context.Background(), ak.UserID, ak.ID, cost, refID, "chat->image refund")
	}

	taskID := image.GenerateTaskID()
	if h.DAO != nil {
		_ = h.DAO.Create(c.Request.Context(), &image.Task{
			TaskID:             taskID,
			UserID:             ak.UserID,
			KeyID:              ak.ID,
			ModelID:            m.ID,
			Prompt:             prompt,
			N:                  1,
			Size:               "1024x1024",
			Operation:          image.OperationGenerate,
			RoutePolicy:        image.RoutePolicyAuto,
			RequestOptionsJSON: image.CanonicalRequest{Operation: image.OperationGenerate, N: 1, Size: "1024x1024", RoutePolicy: image.RoutePolicyAuto}.RequestOptionsJSON(),
			Status:             image.StatusDispatched,
			EstimatedCredit:    cost,
		})
	}

	runCtx, cancel := context.WithTimeout(c.Request.Context(), 6*time.Minute)
	defer cancel()

	res := h.Runner.Run(runCtx, image.RunOptions{
		TaskID:        taskID,
		UserID:        ak.UserID,
		KeyID:         ak.ID,
		ModelID:       m.ID,
		UpstreamModel: m.UpstreamModelSlug,
		Request: image.CanonicalRequest{
			Operation:      image.OperationGenerate,
			Model:          m.Slug,
			Prompt:         maybeAppendClaritySuffix(prompt),
			N:              1,
			Size:           "1024x1024",
			ResponseFormat: image.ResponseFormatURL,
			RoutePolicy:    image.RoutePolicyAuto,
		},
		MaxAttempts: 2,
	})
	rec.AccountID = res.AccountID

	if res.Status != image.StatusSuccess {
		refund(ifEmpty(res.ErrorCode, "upstream_error"))
		httpStatus := http.StatusBadGateway
		if res.ErrorCode == image.ErrNoAccount || res.ErrorCode == image.ErrRateLimited {
			httpStatus = http.StatusServiceUnavailable
		}
		openAIError(c, httpStatus, ifEmpty(res.ErrorCode, "upstream_error"),
			localizeImageErr(res.ErrorCode, res.ErrorMessage))
		return
	}

	if cost > 0 {
		_ = h.Billing.Settle(context.Background(), ak.UserID, ak.ID, cost, cost, refID, "chat->image settle")
	}
	_ = h.Keys.DAO().TouchUsage(context.Background(), ak.ID, c.ClientIP(), cost)
	if h.DAO != nil {
		_ = h.DAO.UpdateCost(c.Request.Context(), taskID, cost)
	}

	rec.Status = usage.StatusSuccess
	rec.CreditCost = cost
	rec.DurationMs = int(time.Since(startAt).Milliseconds())

	resp := ChatCompletionResponse{
		ID:      "chatcmpl-" + uuid.NewString(),
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   m.Slug,
		Choices: []ChatCompletionChoice{{
			Index: 0,
			Message: chatMsg{
				Role:    "assistant",
				Content: buildChatImageMarkdown(taskID, res),
			},
			FinishReason: "stop",
		}},
		Usage: ChatCompletionUsage{
			PromptTokens:     0,
			CompletionTokens: 0,
			TotalTokens:      0,
		},
	}
	c.JSON(http.StatusOK, resp)
}

// extractLastUserPrompt 从 messages 中拿最后一条 user 消息的 content。
func extractLastUserPrompt(msgs []chatMsg) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" && strings.TrimSpace(msgs[i].Content) != "" {
			return msgs[i].Content
		}
	}
	return ""
}

// --- helpers ---

func ifEmpty(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// localizeImageErr 把 runner 返回的英文错误码 + 原始 err.Error() 压成一段中文提示,
// 方便前端 / SDK 用户直接看懂。原始英文 message 作为后缀保留以便排障。
func localizeImageErr(code, raw string) string {
	var zh string
	switch code {
	case image.ErrNoAccount:
		zh = "账号池暂无可用账号,请稍后重试"
	case image.ErrRateLimited:
		zh = "上游风控,请稍后再试"
	case image.ErrPreviewOnly:
		zh = "上游仅返回预览,请稍后重试(已尝试切换账号)"
	case image.ErrInvalidResponse:
		zh = "当前图片路由不支持该请求形态"
	case image.ErrUnknown, "":
		zh = "图片生成失败"
	case "upstream_error":
		zh = "上游返回错误"
	default:
		zh = "图片生成失败(" + code + ")"
	}
	if raw != "" && raw != code {
		return zh + ":" + raw
	}
	return zh
}

func nullableUnix(t *time.Time) int64 {
	if t == nil || t.IsZero() {
		return 0
	}
	return t.Unix()
}

// 含这些关键字时,追加中英双约束让上游出字更清楚(迁移自 gen_image.py)。
var textHintKeywords = []string{
	"文字", "对话", "台词", "旁白", "标语", "字幕", "标题", "文案",
	"招牌", "横幅", "海报文字", "弹幕", "气泡", "字体",
	"text:", "caption", "subtitle", "title:", "label", "banner", "poster text",
}

const claritySuffix = "\n\nclean readable Chinese text, prioritize text clarity over image details"

// ImageEdits 实现 POST /v1/images/edits,严格按 OpenAI 规范接 multipart/form-data。
//
// 表单字段(与 OpenAI 官方一致):
//
//	image            (file)      单张主图,必填
//	image[]          (file)      多张,可重复(2025 起官方支持)
//	mask             (file)      可选,透明区域为编辑区;当前协议下直接一并上传(上游暂不区分)
//	prompt           (string)    必填
//	model            (string)    模型 slug,默认 gpt-image-2
//	n                (int)       默认 1
//	size             (string)    默认 1024x1024
//	response_format  (string)    url | b64_json,当前仅 url
//	user             (string)
//
// 实际走的上游协议和 /v1/images/generations + reference_images 完全相同。
// 行为等价于"把 multipart 文件读成字节 + prompt,交给 ImageGenerations 的主流程"。
func (h *ImagesHandler) ImageEdits(c *gin.Context) {
	startAt := time.Now()
	ak, ok := apikey.FromCtx(c)
	if !ok {
		openAIError(c, http.StatusUnauthorized, "missing_api_key", "缺少 API Key")
		return
	}
	if err := c.Request.ParseMultipartForm(int64(maxReferenceImageBytes) * int64(maxReferenceImages+1)); err != nil {
		openAIError(c, http.StatusBadRequest, "invalid_request_error", "解析 multipart 失败:"+err.Error())
		return
	}

	prompt := strings.TrimSpace(c.Request.FormValue("prompt"))
	if prompt == "" {
		openAIError(c, http.StatusBadRequest, "invalid_request_error", "prompt 不能为空")
		return
	}
	model := c.Request.FormValue("model")
	if model == "" {
		model = "gpt-image-2"
	}
	n := 1
	if s := c.Request.FormValue("n"); s != "" {
		if v, err := parseIntClamp(s, 1, 4); err == nil {
			n = v
		}
	}
	size := c.Request.FormValue("size")
	if size == "" {
		size = "1024x1024"
	}

	baseFiles, maskFile, err := collectEditInputs(c.Request.MultipartForm)
	if err != nil {
		openAIError(c, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	if len(baseFiles) == 0 {
		openAIError(c, http.StatusBadRequest, "invalid_request_error", "至少需要上传一张 image 作为编辑输入")
		return
	}

	baseImages, err := readMultipartInputs(baseFiles)
	if err != nil {
		openAIError(c, http.StatusBadRequest, "invalid_reference_image", err.Error())
		return
	}
	var mask *image.InputImage
	if maskFile != nil {
		img, err := readMultipartInput(maskFile)
		if err != nil {
			openAIError(c, http.StatusBadRequest, "invalid_reference_image", err.Error())
			return
		}
		mask = &img
	}

	canonical := image.CanonicalRequest{
		Operation:      image.OperationEdit,
		Model:          model,
		Prompt:         maybeAppendClaritySuffix(prompt),
		N:              n,
		Size:           size,
		ResponseFormat: c.Request.FormValue("response_format"),
		RoutePolicy:    coalesceRoutePolicy(forcedImageRoute(c), c.Request.FormValue("route_policy")),
		User:           c.Request.FormValue("user"),
		BaseImages:     baseImages,
		Mask:           mask,
		Quality:        c.Request.FormValue("quality"),
		Style:          c.Request.FormValue("style"),
		Background:     c.Request.FormValue("background"),
		OutputFormat:   c.Request.FormValue("output_format"),
		Moderation:     c.Request.FormValue("moderation"),
	}
	if v := c.Request.FormValue("output_compression"); v != "" {
		if iv, err := parseIntClamp(v, 0, 100); err == nil {
			canonical.OutputCompression = iv
		}
	}
	canonical.Normalize()

	m, ratio, rpmCap, rec, refID, fail, ok := h.prepareImageRequest(c, ak, model)
	if !ok {
		return
	}
	defer func() {
		rec.DurationMs = int(time.Since(startAt).Milliseconds())
		if rec.Status == "" {
			rec.Status = usage.StatusFailed
		}
		if h.Usage != nil {
			h.Usage.Write(rec)
		}
	}()
	rec.ModelID = m.ID
	if h.Limiter != nil {
		if ok, _, err := h.Limiter.AllowRPM(c.Request.Context(), ak.ID, rpmCap); err == nil && !ok {
			fail("rate_limit_rpm")
			openAIError(c, http.StatusTooManyRequests, "rate_limit_rpm", "触发每分钟请求数限制 (RPM),请稍后再试")
			return
		}
	}

	cost := billing.ComputeImageCost(m, canonical.N, ratio)
	refunded := false
	refund := func(code string) {
		fail(code)
		if refunded || cost == 0 {
			return
		}
		refunded = true
		_ = h.Billing.Refund(context.Background(), ak.UserID, ak.ID, cost, refID, "image-edit refund")
	}
	if cost > 0 {
		if err := h.Billing.PreDeduct(c.Request.Context(), ak.UserID, ak.ID, cost, refID, "image-edit prepay"); err != nil {
			if errors.Is(err, billing.ErrInsufficient) {
				fail("insufficient_balance")
				openAIError(c, http.StatusPaymentRequired, "insufficient_balance", "积分不足,请前往「账单与充值」充值后再试")
				return
			}
			fail("billing_error")
			openAIError(c, http.StatusInternalServerError, "billing_error", "计费异常:"+err.Error())
			return
		}
	}

	taskID := image.GenerateTaskID()
	if h.DAO != nil {
		if err := h.DAO.Create(c.Request.Context(), &image.Task{
			TaskID:             taskID,
			UserID:             ak.UserID,
			KeyID:              ak.ID,
			ModelID:            m.ID,
			Prompt:             canonical.Prompt,
			N:                  canonical.N,
			Size:               canonical.Size,
			Operation:          canonical.Operation,
			RoutePolicy:        canonical.RoutePolicy,
			RequestOptionsJSON: canonical.RequestOptionsJSON(),
			Status:             image.StatusDispatched,
			EstimatedCredit:    cost,
		}); err != nil {
			refund("billing_error")
			openAIError(c, http.StatusInternalServerError, "internal_error", "创建任务失败:"+err.Error())
			return
		}
	}

	runCtx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Minute)
	defer cancel()
	res := h.Runner.Run(runCtx, image.RunOptions{
		TaskID:        taskID,
		UserID:        ak.UserID,
		KeyID:         ak.ID,
		ModelID:       m.ID,
		UpstreamModel: m.UpstreamModelSlug,
		Request:       canonical,
		MaxAttempts:   2,
	})
	rec.AccountID = res.AccountID
	if res.Status != image.StatusSuccess {
		refund(ifEmpty(res.ErrorCode, "upstream_error"))
		httpStatus := http.StatusBadGateway
		if res.ErrorCode == image.ErrNoAccount || res.ErrorCode == image.ErrRateLimited {
			httpStatus = http.StatusServiceUnavailable
		}
		openAIError(c, httpStatus, ifEmpty(res.ErrorCode, "upstream_error"), localizeImageErr(res.ErrorCode, res.ErrorMessage))
		return
	}

	if cost > 0 {
		if err := h.Billing.Settle(context.Background(), ak.UserID, ak.ID, cost, cost, refID, "image-edit settle"); err != nil {
			logger.L().Error("billing settle image-edit", zap.Error(err), zap.String("ref", refID))
		}
	}
	_ = h.Keys.DAO().TouchUsage(context.Background(), ak.ID, c.ClientIP(), cost)
	rec.Status = usage.StatusSuccess
	rec.CreditCost = cost
	if h.DAO != nil {
		_ = h.DAO.UpdateCost(c.Request.Context(), taskID, cost)
	}
	c.JSON(http.StatusOK, buildImageResponse(taskID, res, canonical.ResponseFormat))
}

// ImageResponsesGenerations 强制走 Responses provider。
func (h *ImagesHandler) ImageResponsesGenerations(c *gin.Context) {
	c.Set(imageRouteOverrideKey, image.RoutePolicyResponses)
	h.ImageGenerations(c)
}

// ImageResponsesEdits 强制走 Responses provider。
func (h *ImagesHandler) ImageResponsesEdits(c *gin.Context) {
	c.Set(imageRouteOverrideKey, image.RoutePolicyResponses)
	h.ImageEdits(c)
}

const imageRouteOverrideKey = "__image_route_override"

func forcedImageRoute(c *gin.Context) string {
	if c == nil {
		return ""
	}
	if v, ok := c.Get(imageRouteOverrideKey); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func coalesceRoutePolicy(values ...string) string {
	for _, v := range values {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

func toInputImages(in []image.ReferenceImage) []image.InputImage {
	out := make([]image.InputImage, 0, len(in))
	for _, img := range in {
		out = append(out, image.InputImage{
			Data:        img.Data,
			FileName:    img.FileName,
			ContentType: sniffImageContentType(img.Data, img.FileName),
		})
	}
	return out
}

func buildImageResponse(taskID string, res *image.RunResult, responseFormat string) ImageGenResponse {
	count := res.OutputCount()
	out := ImageGenResponse{
		Created:   time.Now().Unix(),
		TaskID:    taskID,
		IsPreview: res.IsPreview,
		Data:      make([]ImageGenData, 0, count),
	}
	for i := 0; i < count; i++ {
		d := ImageGenData{}
		if responseFormat == image.ResponseFormatB64JSON {
			if i < len(res.B64JSON) {
				d.B64JSON = res.B64JSON[i]
			}
		} else {
			d.URL = image.BuildImageProxyURL(taskID, i, image.ImageProxyTTL)
		}
		if i < len(res.FileIDs) {
			d.FileID = strings.TrimPrefix(res.FileIDs[i], "sed:")
		}
		if i < len(res.RevisedPrompts) {
			d.RevisedPrompt = res.RevisedPrompts[i]
		}
		out.Data = append(out.Data, d)
	}
	return out
}

func buildChatImageMarkdown(taskID string, res *image.RunResult) string {
	if res == nil {
		return ""
	}
	var sb strings.Builder
	if res.IsPreview {
		// 未命中 IMG2 灰度,只能返回 IMG1 预览,给用户一个明确的软提示
		sb.WriteString("> ⚠️ 本次未使用 IMG2 灰度生成,仅返回预览图。\n\n")
	}
	for i := 0; i < res.OutputCount(); i++ {
		if i > 0 {
			sb.WriteString("\n\n")
		}
		sb.WriteString(fmt.Sprintf("![generated](%s)", image.BuildImageProxyURL(taskID, i, image.ImageProxyTTL)))
	}
	return sb.String()
}

func (h *ImagesHandler) prepareImageRequest(c *gin.Context, ak *apikey.APIKey, model string) (
	*modelpkg.Model, float64, int, *usage.Log, string, func(string), bool,
) {
	refID := uuid.NewString()
	rec := &usage.Log{
		UserID:    ak.UserID,
		KeyID:     ak.ID,
		RequestID: refID,
		Type:      usage.TypeImage,
		IP:        c.ClientIP(),
		UA:        c.Request.UserAgent(),
	}
	fail := func(code string) { rec.Status = usage.StatusFailed; rec.ErrorCode = code }

	if !ak.ModelAllowed(model) {
		fail("model_not_allowed")
		openAIError(c, http.StatusForbidden, "model_not_allowed",
			fmt.Sprintf("当前 API Key 无权调用模型 %q", model))
		return nil, 0, 0, rec, refID, fail, false
	}
	m, err := h.Models.BySlug(c.Request.Context(), model)
	if err != nil || m == nil || !m.Enabled {
		fail("model_not_found")
		openAIError(c, http.StatusBadRequest, "model_not_found",
			fmt.Sprintf("模型 %q 不存在或已下架", model))
		return nil, 0, 0, rec, refID, fail, false
	}
	if m.Type != modelpkg.TypeImage {
		fail("model_type_mismatch")
		openAIError(c, http.StatusBadRequest, "model_type_mismatch",
			fmt.Sprintf("模型 %q 不是图像模型", model))
		return nil, 0, 0, rec, refID, fail, false
	}

	ratio := 1.0
	rpmCap := ak.RPM
	if h.Groups != nil {
		if g, err := h.Groups.OfUser(c.Request.Context(), ak.UserID); err == nil && g != nil {
			ratio = g.Ratio
			if rpmCap == 0 {
				rpmCap = g.RPMLimit
			}
		}
	}
	return m, ratio, rpmCap, rec, refID, fail, true
}

// collectEditInputs 把 multipart 中的 base images 与 mask 分开。
func collectEditInputs(form *multipart.Form) ([]*multipart.FileHeader, *multipart.FileHeader, error) {
	if form == nil {
		return nil, nil, errors.New("empty multipart form")
	}
	var base []*multipart.FileHeader
	var mask *multipart.FileHeader
	seen := map[string]bool{}
	addBase := func(fhs []*multipart.FileHeader) {
		for _, fh := range fhs {
			if fh == nil {
				continue
			}
			key := fh.Filename + "|" + fmt.Sprint(fh.Size)
			if seen[key] {
				continue
			}
			seen[key] = true
			base = append(base, fh)
		}
	}
	for _, key := range []string{"image", "image[]", "images", "images[]"} {
		if fhs := form.File[key]; len(fhs) > 0 {
			addBase(fhs)
		}
	}
	for _, key := range []string{"mask", "mask[]"} {
		if fhs := form.File[key]; len(fhs) > 0 && fhs[0] != nil {
			mask = fhs[0]
			break
		}
	}
	// 也兼容 image_1 / image_2 / ... 的写法
	for k, fhs := range form.File {
		if strings.HasPrefix(k, "image_") {
			addBase(fhs)
		}
	}
	return base, mask, nil
}

func readMultipartInputs(files []*multipart.FileHeader) ([]image.InputImage, error) {
	out := make([]image.InputImage, 0, len(files))
	for _, fh := range files {
		img, err := readMultipartInput(fh)
		if err != nil {
			return nil, err
		}
		out = append(out, img)
	}
	return out, nil
}

func readMultipartInput(fh *multipart.FileHeader) (image.InputImage, error) {
	data, err := readMultipart(fh)
	if err != nil {
		return image.InputImage{}, fmt.Errorf("读取 %q 失败:%s", fh.Filename, err.Error())
	}
	if len(data) == 0 {
		return image.InputImage{}, fmt.Errorf("参考图 %q 为空", fh.Filename)
	}
	if len(data) > maxReferenceImageBytes {
		return image.InputImage{}, fmt.Errorf("参考图 %q 超过 %dMB 上限", fh.Filename, maxReferenceImageBytes/1024/1024)
	}
	return image.InputImage{
		Data:        data,
		FileName:    filepath.Base(fh.Filename),
		ContentType: sniffImageContentType(data, fh.Filename),
	}, nil
}

func sniffImageContentType(data []byte, fileName string) string {
	ct := http.DetectContentType(data)
	if strings.HasPrefix(ct, "image/") {
		return ct
	}
	switch strings.ToLower(filepath.Ext(fileName)) {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".webp":
		return "image/webp"
	default:
		return "image/png"
	}
}

func readMultipart(fh *multipart.FileHeader) ([]byte, error) {
	f, err := fh.Open()
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}

// decodeReferenceInputs 把 JSON 里 reference_images(url/data-url/base64 混合)下载/解码成字节。
// 超出条数上限直接报错;单张尺寸上限 maxReferenceImageBytes。
func decodeReferenceInputs(ctx context.Context, inputs []string) ([]image.ReferenceImage, error) {
	if len(inputs) == 0 {
		return nil, nil
	}
	if len(inputs) > maxReferenceImages {
		return nil, fmt.Errorf("最多支持 %d 张参考图", maxReferenceImages)
	}
	out := make([]image.ReferenceImage, 0, len(inputs))
	for i, s := range inputs {
		s = strings.TrimSpace(s)
		if s == "" {
			return nil, fmt.Errorf("第 %d 张参考图为空", i+1)
		}
		data, name, err := fetchReferenceBytes(ctx, s)
		if err != nil {
			return nil, fmt.Errorf("第 %d 张参考图:%w", i+1, err)
		}
		if len(data) == 0 {
			return nil, fmt.Errorf("第 %d 张参考图解码后为空", i+1)
		}
		if len(data) > maxReferenceImageBytes {
			return nil, fmt.Errorf("第 %d 张参考图超过 %dMB 上限", i+1, maxReferenceImageBytes/1024/1024)
		}
		out = append(out, image.ReferenceImage{Data: data, FileName: name})
	}
	return out, nil
}

// fetchReferenceBytes 支持 http(s) / data URL / 裸 base64 三种输入。
func fetchReferenceBytes(ctx context.Context, s string) ([]byte, string, error) {
	low := strings.ToLower(s)
	switch {
	case strings.HasPrefix(low, "data:"):
		// data:<mime>[;base64],<payload>
		comma := strings.IndexByte(s, ',')
		if comma < 0 {
			return nil, "", errors.New("无效 data URL")
		}
		meta := s[5:comma]
		payload := s[comma+1:]
		if strings.Contains(strings.ToLower(meta), ";base64") {
			b, err := base64.StdEncoding.DecodeString(payload)
			if err != nil {
				// 兼容 URL-safe
				if b2, err2 := base64.URLEncoding.DecodeString(payload); err2 == nil {
					b = b2
				} else {
					return nil, "", fmt.Errorf("base64 解码失败:%w", err)
				}
			}
			return b, "", nil
		}
		return []byte(payload), "", nil
	case strings.HasPrefix(low, "http://"), strings.HasPrefix(low, "https://"):
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, s, nil)
		if err != nil {
			return nil, "", err
		}
		// 15s 基本能覆盖 OSS / CDN / presigned URL
		hc := &http.Client{Timeout: 15 * time.Second}
		res, err := hc.Do(req)
		if err != nil {
			return nil, "", err
		}
		defer res.Body.Close()
		if res.StatusCode >= 400 {
			return nil, "", fmt.Errorf("下载失败 HTTP %d", res.StatusCode)
		}
		body, err := io.ReadAll(io.LimitReader(res.Body, int64(maxReferenceImageBytes)+1))
		if err != nil {
			return nil, "", err
		}
		name := filepath.Base(req.URL.Path)
		return body, name, nil
	default:
		// 当成裸 base64 处理
		b, err := base64.StdEncoding.DecodeString(s)
		if err != nil {
			if b2, err2 := base64.URLEncoding.DecodeString(s); err2 == nil {
				return b2, "", nil
			}
			return nil, "", fmt.Errorf("既非 URL 也非可解析的 base64:%w", err)
		}
		return b, "", nil
	}
}

func parseIntClamp(s string, min, max int) (int, error) {
	var v int
	if _, err := fmt.Sscanf(s, "%d", &v); err != nil {
		return 0, err
	}
	if v < min {
		v = min
	}
	if v > max {
		v = max
	}
	return v, nil
}

func maybeAppendClaritySuffix(prompt string) string {
	lower := strings.ToLower(prompt)
	need := false
	for _, kw := range textHintKeywords {
		if strings.Contains(lower, strings.ToLower(kw)) {
			need = true
			break
		}
	}
	if !need {
		// 检测中文/英文引号内容 ≥ 2 个字
		for _, pair := range [][2]string{
			{"\"", "\""}, {"'", "'"},
			{"“", "”"}, {"‘", "’"},
			{"「", "」"}, {"『", "』"},
		} {
			if idx := strings.Index(prompt, pair[0]); idx >= 0 {
				rest := prompt[idx+len(pair[0]):]
				if end := strings.Index(rest, pair[1]); end >= 2 {
					need = true
					break
				}
			}
		}
	}
	if need && !strings.Contains(prompt, strings.TrimSpace(claritySuffix)) {
		return prompt + claritySuffix
	}
	return prompt
}
