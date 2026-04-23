package image

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"
)

var imageProxySecret []byte

func init() {
	imageProxySecret = make([]byte, 32)
	if _, err := rand.Read(imageProxySecret); err != nil {
		for i := range imageProxySecret {
			imageProxySecret[i] = byte(i*31 + 7)
		}
	}
}

// ImageProxyTTL 单条签名 URL 的默认有效期。
const ImageProxyTTL = 24 * time.Hour

// BuildImageProxyURL 生成图片代理 URL。返回绝对 path(不含 host)。
func BuildImageProxyURL(taskID string, idx int, ttl time.Duration) string {
	if ttl <= 0 {
		ttl = ImageProxyTTL
	}
	expMs := time.Now().Add(ttl).UnixMilli()
	sig := computeImageSig(taskID, idx, expMs)
	return fmt.Sprintf("/p/img/%s/%d?exp=%d&sig=%s", taskID, idx, expMs, sig)
}

// VerifyImageProxyURL 校验图片代理 URL 的签名和过期时间。
func VerifyImageProxyURL(taskID string, idx int, expMs int64, sig string) bool {
	if expMs < time.Now().UnixMilli() {
		return false
	}
	want := computeImageSig(taskID, idx, expMs)
	return hmac.Equal([]byte(sig), []byte(want))
}

// BuildTaskImageProxyURLs 为任务产物生成可访问的代理 URL。
func BuildTaskImageProxyURLs(taskID string, outputs []TaskOutput, fileIDs, resultURLs []string, ttl time.Duration) []string {
	if len(outputs) > 0 {
		out := make([]string, 0, len(outputs))
		for _, item := range outputs {
			out = append(out, BuildImageProxyURL(taskID, item.OutputIndex, ttl))
		}
		return out
	}
	count := len(fileIDs)
	if len(resultURLs) > count {
		count = len(resultURLs)
	}
	if count == 0 {
		return nil
	}
	out := make([]string, 0, count)
	for i := 0; i < count; i++ {
		out = append(out, BuildImageProxyURL(taskID, i, ttl))
	}
	return out
}

func computeImageSig(taskID string, idx int, expMs int64) string {
	mac := hmac.New(sha256.New, imageProxySecret)
	fmt.Fprintf(mac, "%s|%d|%d", taskID, idx, expMs)
	return hex.EncodeToString(mac.Sum(nil))[:24]
}
