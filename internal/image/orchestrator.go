package image

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/432539/gpt2api/internal/scheduler"
	openaiup "github.com/432539/gpt2api/internal/upstream/openai"
)

// Run 是图片执行统一入口。
func (r *Runner) Run(ctx context.Context, opt RunOptions) *RunResult {
	start := time.Now()
	req := opt.Request
	req.Normalize()

	result := &RunResult{
		Status:      StatusFailed,
		ErrorCode:   ErrUnknown,
		RoutePolicy: req.RoutePolicy,
	}
	if r.dao != nil && opt.TaskID != "" {
		_ = r.dao.MarkRunning(ctx, opt.TaskID, 0)
	}

	plan, err := PlanRoute(req, RouteConfigFromProvider(r.routeCfg))
	if err != nil {
		result.ErrorCode = ErrInvalidResponse
		result.ErrorMessage = err.Error()
		result.DurationMs = time.Since(start).Milliseconds()
		if r.dao != nil && opt.TaskID != "" {
			_ = r.dao.MarkFailed(ctx, opt.TaskID, result.ErrorCode)
		}
		return result
	}

	for idx, provider := range plan.Providers {
		var current *RunResult
		switch provider {
		case ProviderReverse:
			current = r.runReverseFanout(ctx, opt)
		case ProviderNative, ProviderResponses:
			current = r.runAPIProvider(ctx, provider, opt)
		default:
			continue
		}
		if current == nil {
			continue
		}
		current.SwitchCount = idx
		if current.Status == StatusSuccess {
			current.DurationMs = time.Since(start).Milliseconds()
			if r.dao != nil && opt.TaskID != "" {
				_ = r.dao.SetExecutionMeta(ctx, opt.TaskID, req.Operation, current.ProviderKind,
					req.RoutePolicy, req.RequestOptionsJSON(), current.Attempts, current.SwitchCount, current.AccountID)
				if len(current.Outputs) > 0 {
					_ = r.dao.ReplaceOutputs(ctx, opt.TaskID, current.Outputs)
				}
				_ = r.dao.MarkSuccess(ctx, opt.TaskID, current.ConversationID, current.FileIDs, current.SignedURLs, 0)
			}
			return current
		}
		result = current
	}

	result.DurationMs = time.Since(start).Milliseconds()
	if r.dao != nil && opt.TaskID != "" {
		_ = r.dao.SetExecutionMeta(ctx, opt.TaskID, req.Operation, result.ProviderKind,
			req.RoutePolicy, req.RequestOptionsJSON(), result.Attempts, result.SwitchCount, result.AccountID)
		_ = r.dao.MarkFailed(ctx, opt.TaskID, result.ErrorCode)
	}
	return result
}

func (r *Runner) runReverseFanout(ctx context.Context, opt RunOptions) *RunResult {
	req := opt.Request
	req.Normalize()
	if req.N <= 1 {
		return r.runReverse(ctx, opt)
	}
	merged := &RunResult{
		Status:       StatusSuccess,
		ProviderKind: ProviderReverse,
		RoutePolicy:  req.RoutePolicy,
		SignedURLs:   []string{},
		FileIDs:      []string{},
		ContentTypes: []string{},
	}
	for i := 0; i < req.N; i++ {
		next := opt
		next.Request = singleImageRequest(req)
		res := r.runReverse(ctx, next)
		if res == nil || res.Status != StatusSuccess {
			if merged.Attempts == 0 {
				return res
			}
			merged.Status = StatusFailed
			merged.ErrorCode = res.ErrorCode
			merged.ErrorMessage = res.ErrorMessage
			return merged
		}
		merged.AccountID = res.AccountID
		merged.Attempts += res.Attempts
		merged.IsPreview = merged.IsPreview || res.IsPreview
		merged.FileIDs = append(merged.FileIDs, res.FileIDs...)
		merged.SignedURLs = append(merged.SignedURLs, res.SignedURLs...)
		merged.ContentTypes = append(merged.ContentTypes, res.ContentTypes...)
	}
	return merged
}

func (r *Runner) runAPIProvider(ctx context.Context, provider string, opt RunOptions) *RunResult {
	req := opt.Request
	req.Normalize()
	result := &RunResult{
		Status:       StatusFailed,
		ErrorCode:    ErrUnknown,
		ProviderKind: provider,
		RoutePolicy:  req.RoutePolicy,
	}
	excluded := map[uint64]struct{}{}

	for switchCount := 0; switchCount < 4; switchCount++ {
		lease, err := r.sched.DispatchImage(ctx, scheduler.DispatchSpec{
			Operation:           req.Operation,
			PreferredProvider:   provider,
			AllowedProviders:    []string{provider},
			ExcludedAccountIDs:  excluded,
			NeedMaskSupport:     req.Mask != nil,
			NeedNativeOptions:   req.HasNativeOnlyOptions(),
			NeedB64JSON:         req.NeedsB64JSON(),
			NeedReferenceImages: req.HasReferenceImages(),
		})
		if err != nil {
			result.ErrorCode = ErrNoAccount
			result.ErrorMessage = err.Error()
			return result
		}
		result.AccountID = lease.Account.ID
		retryLimit := lease.Account.SameAccountRetryLimit
		if retryLimit <= 0 {
			retryLimit = 1
		}

		for attempt := 1; attempt <= retryLimit; attempt++ {
			result.Attempts++
			cur, retryable := r.executeAPIProviderOnce(ctx, provider, lease, opt)
			if cur != nil && cur.Status == StatusSuccess {
				_ = lease.Release(context.Background())
				cur.Attempts = result.Attempts
				cur.SwitchCount = switchCount
				return cur
			}
			if cur != nil {
				result.ErrorCode = cur.ErrorCode
				result.ErrorMessage = cur.ErrorMessage
			}
			if !retryable || attempt >= retryLimit {
				break
			}
		}

		excluded[lease.Account.ID] = struct{}{}
		result.SwitchCount = switchCount + 1
		_ = lease.Release(context.Background())
	}
	return result
}

func (r *Runner) executeAPIProviderOnce(ctx context.Context, provider string, lease *scheduler.Lease, opt RunOptions) (*RunResult, bool) {
	req := opt.Request
	client, err := openaiup.New(lease.APIBaseURL, lease.APIKey, lease.ProxyURL, 90*time.Second)
	if err != nil {
		return &RunResult{
			Status:       StatusFailed,
			ProviderKind: provider,
			RoutePolicy:  req.RoutePolicy,
			AccountID:    lease.Account.ID,
			ErrorCode:    ErrUnknown,
			ErrorMessage: err.Error(),
		}, false
	}

	var resp *openaiup.ImageResponse
	switch provider {
	case ProviderNative:
		if req.Operation == OperationGenerate && len(req.BaseImages) == 0 && len(req.ReferenceImages) == 0 {
			resp, err = client.ImagesGenerate(ctx, toOpenAIRequest(req), opt.UpstreamModel)
		} else {
			resp, err = client.ImagesEdit(ctx, toOpenAIRequest(req), opt.UpstreamModel)
		}
	case ProviderResponses:
		if SupportLevel(provider, req) == SupportRejected {
			return &RunResult{
				Status:       StatusFailed,
				ProviderKind: provider,
				RoutePolicy:  req.RoutePolicy,
				AccountID:    lease.Account.ID,
				ErrorCode:    ErrInvalidResponse,
				ErrorMessage: "responses route does not support this image request shape",
			}, false
		}
		resp, err = client.ResponsesImage(ctx, toOpenAIRequest(singleImageRequest(req)), responsesModel(opt.UpstreamModel))
		if err == nil && req.N > 1 {
			merged := make([]openaiup.ImageData, 0, req.N)
			if resp != nil {
				merged = append(merged, resp.Data...)
			}
			for i := 1; i < req.N; i++ {
				more, moreErr := client.ResponsesImage(ctx, toOpenAIRequest(singleImageRequest(req)), responsesModel(opt.UpstreamModel))
				if moreErr != nil {
					err = moreErr
					break
				}
				merged = append(merged, more.Data...)
			}
			if resp != nil {
				resp.Data = merged
			}
		}
	default:
		err = fmt.Errorf("unsupported provider: %s", provider)
	}
	if err != nil {
		code := r.handleAPIProviderFailure(lease.Account.ID, err)
		return &RunResult{
			Status:       StatusFailed,
			ProviderKind: provider,
			RoutePolicy:  req.RoutePolicy,
			AccountID:    lease.Account.ID,
			ErrorCode:    code,
			ErrorMessage: err.Error(),
		}, shouldRetryAPIProvider(err)
	}

	out, convErr := r.persistProviderOutputs(ctx, provider, opt.TaskID, req, client, resp)
	if convErr != nil {
		return &RunResult{
			Status:       StatusFailed,
			ProviderKind: provider,
			RoutePolicy:  req.RoutePolicy,
			AccountID:    lease.Account.ID,
			ErrorCode:    ErrDownload,
			ErrorMessage: convErr.Error(),
		}, true
	}
	out.AccountID = lease.Account.ID
	out.ProviderKind = provider
	out.RoutePolicy = req.RoutePolicy
	return out, false
}

func (r *Runner) persistProviderOutputs(ctx context.Context, provider, taskID string, req CanonicalRequest,
	client *openaiup.Client, resp *openaiup.ImageResponse) (*RunResult, error) {
	if resp == nil || len(resp.Data) == 0 {
		return nil, fmt.Errorf("provider returned no image outputs")
	}
	result := &RunResult{
		Status:         StatusSuccess,
		ProviderKind:   provider,
		RoutePolicy:    req.RoutePolicy,
		SignedURLs:     make([]string, 0, len(resp.Data)),
		ContentTypes:   make([]string, 0, len(resp.Data)),
		B64JSON:        make([]string, 0, len(resp.Data)),
		RevisedPrompts: make([]string, 0, len(resp.Data)),
		Outputs:        make([]TaskOutput, 0, len(resp.Data)),
	}
	for idx, item := range resp.Data {
		var (
			body        []byte
			contentType string
			err         error
		)
		switch {
		case item.B64JSON != "":
			body, err = base64.StdEncoding.DecodeString(item.B64JSON)
			if err != nil {
				return nil, err
			}
			contentType = contentTypeForRequest(req)
		case item.URL != "":
			body, contentType, err = client.Download(ctx, item.URL)
			if err != nil {
				return nil, err
			}
			if contentType == "" {
				contentType = contentTypeForRequest(req)
			}
		default:
			return nil, fmt.Errorf("image output missing url and b64_json")
		}

		ref := item.URL
		sourceType := "remote_url"
		if r.storage != nil && taskID != "" {
			ref, err = r.storage.Save(taskID, idx, contentType, body)
			if err != nil {
				return nil, err
			}
			sourceType = "stored_blob"
		}
		result.Outputs = append(result.Outputs, TaskOutput{
			TaskID:        taskID,
			OutputIndex:   idx,
			SourceType:    sourceType,
			SourceRef:     ref,
			ContentType:   contentType,
			RevisedPrompt: item.RevisedPrompt,
		})
		result.SignedURLs = append(result.SignedURLs, "")
		result.ContentTypes = append(result.ContentTypes, contentType)
		if req.ResponseFormat == ResponseFormatB64JSON {
			if item.B64JSON != "" {
				result.B64JSON = append(result.B64JSON, item.B64JSON)
			} else {
				result.B64JSON = append(result.B64JSON, base64.StdEncoding.EncodeToString(body))
			}
		}
		result.RevisedPrompts = append(result.RevisedPrompts, item.RevisedPrompt)
	}
	return result, nil
}

func singleImageRequest(in CanonicalRequest) CanonicalRequest {
	out := in
	out.N = 1
	return out
}

func responsesModel(upstream string) string {
	upstream = strings.TrimSpace(upstream)
	if upstream == "" || upstream == "auto" || strings.HasPrefix(upstream, "gpt-image") {
		return "gpt-4.1-mini"
	}
	return upstream
}

func toOpenAIRequest(req CanonicalRequest) openaiup.ImageRequest {
	out := openaiup.ImageRequest{
		Operation:         req.Operation,
		Model:             req.Model,
		Prompt:            req.Prompt,
		N:                 req.N,
		Size:              req.Size,
		ResponseFormat:    req.ResponseFormat,
		Quality:           req.Quality,
		Style:             req.Style,
		Background:        req.Background,
		OutputFormat:      req.OutputFormat,
		OutputCompression: req.OutputCompression,
		Moderation:        req.Moderation,
		BaseImages:        make([]openaiup.InputImage, 0, len(req.BaseImages)),
		ReferenceImages:   make([]openaiup.InputImage, 0, len(req.ReferenceImages)),
	}
	for _, img := range req.BaseImages {
		out.BaseImages = append(out.BaseImages, openaiup.InputImage{
			Data: img.Data, FileName: img.FileName, ContentType: img.ContentType,
		})
	}
	for _, img := range req.ReferenceImages {
		out.ReferenceImages = append(out.ReferenceImages, openaiup.InputImage{
			Data: img.Data, FileName: img.FileName, ContentType: img.ContentType,
		})
	}
	if req.Mask != nil {
		out.Mask = &openaiup.InputImage{
			Data: req.Mask.Data, FileName: req.Mask.FileName, ContentType: req.Mask.ContentType,
		}
	}
	return out
}

func contentTypeForRequest(req CanonicalRequest) string {
	switch strings.ToLower(strings.TrimSpace(req.OutputFormat)) {
	case "jpeg", "jpg":
		return "image/jpeg"
	case "webp":
		return "image/webp"
	default:
		return "image/png"
	}
}

func classifyAPIProviderError(err error) string {
	if err == nil {
		return ""
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "http=429"), strings.Contains(msg, "http=503"):
		return ErrRateLimited
	case strings.Contains(msg, "http=401"), strings.Contains(msg, "http=403"):
		return ErrAuthRequired
	default:
		return ErrUpstream
	}
}

func notifyAPIProviderFailure(err error, onRateLimited func(), onAuthRequired func()) string {
	code := classifyAPIProviderError(err)
	switch code {
	case ErrRateLimited:
		if onRateLimited != nil {
			onRateLimited()
		}
	case ErrAuthRequired:
		if onAuthRequired != nil {
			onAuthRequired()
		}
	}
	return code
}

func (r *Runner) handleAPIProviderFailure(accountID uint64, err error) string {
	return notifyAPIProviderFailure(err, func() {
		if r != nil && r.sched != nil && accountID > 0 {
			r.sched.MarkRateLimited(context.Background(), accountID)
		}
	}, func() {
		if r != nil && r.sched != nil && accountID > 0 {
			r.sched.MarkDead(context.Background(), accountID)
		}
	})
}

func shouldRetryAPIProvider(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "timeout") ||
		strings.Contains(msg, "deadline exceeded") ||
		strings.Contains(msg, "http=429") ||
		strings.Contains(msg, "http=500") ||
		strings.Contains(msg, "http=502") ||
		strings.Contains(msg, "http=503") ||
		strings.Contains(msg, "http=504")
}
