package image

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/432539/gpt2api/internal/scheduler"
	"github.com/432539/gpt2api/internal/upstream/chatgpt"
	"github.com/432539/gpt2api/pkg/logger"
)

// Runner 单次/多次生图的执行器。封装完整的 chatgpt.com 协议链路:
//
//	ChatRequirements → (InitConversation) → PrepareFConversation →
//	StreamFConversation (SSE) → ParseImageSSE → PollConversationForImages →
//	ImageDownloadURL(签名 URL)
//
// 灰度桶未命中(preview_only)会自动换账号重试。
type Runner struct {
	sched *scheduler.Scheduler
	dao   *DAO
	storage *Storage
	routeCfg ImageRouteConfigProvider
}

// NewRunner 构造 Runner。
func NewRunner(sched *scheduler.Scheduler, dao *DAO, storage *Storage) *Runner {
	return &Runner{sched: sched, dao: dao, storage: storage}
}

// ReferenceImage 是图生图/编辑的一张参考图输入。
// 只需要提供原始字节 + 可选的文件名,Runner 会在运行时调用 chatgpt Client 上传。
type ReferenceImage struct {
	Data     []byte
	FileName string // 可选,未填时按长度 + 嗅探扩展名生成
}

// RunOptions 是单次生图的输入。
type RunOptions struct {
	TaskID            string
	UserID            uint64
	KeyID             uint64
	ModelID           uint64
	UpstreamModel     string // 默认 "auto"(由上游根据 system_hints 挑选图像模型)
	Request           CanonicalRequest
	MaxAttempts       int                // 灰度未命中时最大重试,默认 2
	PerAttemptTimeout time.Duration      // 单次尝试总超时,默认 5min
	PollMaxWait       time.Duration      // 轮询最长等待,默认 300s
}

// RunResult 是单次生图的输出。
type RunResult struct {
	Status         string   // success / failed
	ConversationID string
	AccountID      uint64
	FileIDs        []string // chatgpt.com 侧的原始 ref("sed:" 前缀表示 sediment)
	SignedURLs     []string // 直接可访问的签名 URL(15 分钟有效)
	ContentTypes   []string
	B64JSON        []string
	RevisedPrompts []string
	ProviderKind   string
	RoutePolicy    string
	Outputs        []TaskOutput
	ErrorCode      string
	ErrorMessage   string
	Attempts       int   // 跨账号尝试次数(runOnce 次数)
	SwitchCount    int
	TurnsInConv    int   // 当前账号内同会话 picture_v2 轮次
	IsPreview      bool  // true=返回的是 IMG1 sediment 预览(3 轮均未命中 IMG2 灰度,已尽力)
	DurationMs     int64
}

// SetRouteConfigProvider 注入图片路由开关。
func (r *Runner) SetRouteConfigProvider(p ImageRouteConfigProvider) { r.routeCfg = p }

// runReverse 执行 reverse provider。
func (r *Runner) runReverse(ctx context.Context, opt RunOptions) *RunResult {
	start := time.Now()
	opt.Request.Normalize()
	if opt.MaxAttempts <= 0 {
		opt.MaxAttempts = 2
	}
	if opt.PerAttemptTimeout <= 0 {
		opt.PerAttemptTimeout = 5 * time.Minute
	}
	if opt.PollMaxWait <= 0 {
		opt.PollMaxWait = 300 * time.Second
	}
	if opt.UpstreamModel == "" {
		// 对齐浏览器抓包 + 参考实现:图像走 f/conversation 时 model 字段和
		// 普通 chat 一致用 "auto",通过 system_hints=["picture_v2"] 让上游知道
		// 这是图像任务。硬写 "gpt-5-3" 在免费/新账号上会直接 404。
		opt.UpstreamModel = "auto"
	}
	if opt.Request.N <= 0 {
		opt.Request.N = 1
	}

	result := &RunResult{
		Status:       StatusFailed,
		ErrorCode:    ErrUnknown,
		ProviderKind: ProviderReverse,
		RoutePolicy:  opt.Request.RoutePolicy,
	}

	for attempt := 1; attempt <= opt.MaxAttempts; attempt++ {
		result.Attempts = attempt
		if err := ctx.Err(); err != nil {
			result.ErrorCode = ErrUnknown
			result.ErrorMessage = err.Error()
			break
		}

		attemptCtx, cancel := context.WithTimeout(ctx, opt.PerAttemptTimeout)
		ok, status, err := r.runOnce(attemptCtx, opt, result)
		cancel()

		if ok {
			result.Status = StatusSuccess
			result.ErrorCode = ""
			result.ErrorMessage = ""
			break
		}
		// 记录本次失败原因
		if err != nil {
			result.ErrorMessage = err.Error()
		}
		result.ErrorCode = status

		// preview_only:换账号重试;其他错误直接退出
		if status != ErrPreviewOnly {
			break
		}
		logger.L().Info("image runner preview_only, retry with another account",
			zap.String("task_id", opt.TaskID), zap.Int("attempt", attempt))
	}

	result.DurationMs = time.Since(start).Milliseconds()
	return result
}

// runOnce 一次完整的尝试。返回 (ok, errorCode, err)。
// result 会被就地更新(ConversationID / FileIDs / SignedURLs / AccountID 等)。
func (r *Runner) runOnce(ctx context.Context, opt RunOptions, result *RunResult) (bool, string, error) {
	// 1) 调度账号
	lease, err := r.sched.DispatchImage(ctx, scheduler.DispatchSpec{
		Operation:           opt.Request.Operation,
		PreferredProvider:   ProviderReverse,
		AllowedProviders:    []string{ProviderReverse},
		NeedReferenceImages: opt.Request.HasReferenceImages(),
	})
	if err != nil {
		if errors.Is(err, scheduler.ErrNoAvailable) {
			return false, ErrNoAccount, err
		}
		return false, ErrUnknown, err
	}
	defer func() {
		_ = lease.Release(context.Background())
	}()
	result.AccountID = lease.Account.ID
	// 立刻把 account_id 写回 image_tasks,供后续图片代理端点按 task_id 解出 AT。
	// MarkRunning 在 status=running 时 WHERE 不命中,所以用专门的 SetAccount。
	if r.dao != nil && opt.TaskID != "" {
		_ = r.dao.SetAccount(ctx, opt.TaskID, lease.Account.ID)
	}

	// 2) 构造上游 client
	cli, err := chatgpt.New(chatgpt.Options{
		AuthToken: lease.AuthToken,
		DeviceID:  lease.DeviceID,
		SessionID: lease.SessionID,
		ProxyURL:  lease.ProxyURL,
		Cookies:   "", // 目前不从 oai_account_cookies 加载,后续 M3+ 再做
	})
	if err != nil {
		return false, ErrUnknown, fmt.Errorf("chatgpt client: %w", err)
	}

	// 3) ChatRequirements + POW(新两步 sentinel 流程,solver 未配置时内部自动
	// 回退到单步接口)
	cr, err := cli.ChatRequirementsV2(ctx)
	if err != nil {
		return false, r.classifyUpstream(err), err
	}
	var proofToken string
	if cr.Proofofwork.Required {
		proofCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
		ch := make(chan string, 1)
		go func() { ch <- cr.SolveProof(chatgpt.DefaultUserAgent) }()
		select {
		case <-proofCtx.Done():
			cancel()
			r.sched.MarkWarned(context.Background(), lease.Account.ID)
			return false, ErrPOWTimeout, proofCtx.Err()
		case proofToken = <-ch:
			cancel()
		}
		if proofToken == "" {
			r.sched.MarkWarned(context.Background(), lease.Account.ID)
			return false, ErrPOWFailed, errors.New("pow solver returned empty")
		}
	}
	// Turnstile 是"建议性"信号:即使服务端声明 required,只要 chat_token + proof_token
	// 齐全,绝大多数账号的 f/conversation 仍然会正常下发图片结果。与 chat 流程(gateway/chat.go)
	// 保持一致——只打 warn,不阻断;真正拿不到 IMG2 终稿时由后续 poll 逻辑判定为失败。
	if cr.Turnstile.Required {
		logger.L().Warn("image turnstile required, continue anyway",
			zap.Uint64("account_id", lease.Account.ID))
	}

	// 4) 不再调用 /backend-api/conversation/init:
	// 浏览器实测路径是 prepare → chat-requirements → f/conversation 三步,init 是
	// 过时/冗余调用,在免费账号上还会返回 404 让整条链路 fail。system_hints=picture_v2
	// 会通过 f/conversation 的 payload 字段传达。

	// 4.5) 图生图:上传参考图。任何一张失败都直接整体 fail(上游后续会对不上 attachment)。
	refsInput := make([]InputImage, 0, len(opt.Request.BaseImages)+len(opt.Request.ReferenceImages))
	refsInput = append(refsInput, opt.Request.BaseImages...)
	refsInput = append(refsInput, opt.Request.ReferenceImages...)
	var refs []*chatgpt.UploadedFile
	if len(refsInput) > 0 {
		for idx, r0 := range refsInput {
			upCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
			up, err := cli.UploadFile(upCtx, r0.Data, r0.FileName)
			cancel()
			if err != nil {
				logger.L().Warn("image runner upload reference failed",
					zap.Int("idx", idx), zap.Error(err))
				if ue, ok := err.(*chatgpt.UpstreamError); ok && ue.IsRateLimited() {
					r.sched.MarkRateLimited(context.Background(), lease.Account.ID)
					return false, ErrRateLimited, err
				}
				return false, ErrUpstream, fmt.Errorf("upload reference %d: %w", idx, err)
			}
			refs = append(refs, up)
		}
		logger.L().Info("image runner references uploaded",
			zap.String("task_id", opt.TaskID), zap.Int("count", len(refs)))
	}

	// 注意:新会话不要本地生成 conversation_id,上游会 404。
	// 真正的 conv_id 由 SSE 的 resume_conversation_token / sseResult.ConversationID 返回。
	var convID string
	parentID := uuid.NewString()
	messageID := uuid.NewString()

	// 统一把 model 强制为 "auto":对齐参考实现(只通过 system_hints=["picture_v2"]
	// 区分图像任务),避免 chatgpt-freeaccount / chatgpt-paid 之间的 model slug 差异。
	upstreamModel := "auto"
	if opt.UpstreamModel != "" && opt.UpstreamModel != "auto" && !cr.IsFreeAccount() {
		// 付费账号如果明确传了 upstream slug 且不是 auto,可以尊重用户传入
		// (但我们现有模型库没有 image 专用 slug,保留扩展点)
		upstreamModel = opt.UpstreamModel
	} else if cr.IsFreeAccount() && opt.UpstreamModel != "" && opt.UpstreamModel != "auto" {
		logger.L().Warn("image: free account requesting premium model, downgrade to auto",
			zap.Uint64("account_id", lease.Account.ID),
			zap.String("requested_model", opt.UpstreamModel))
	}

	// 5-7) 同账号 + 同会话多轮发起 picture_v2,命中 IMG2 即返回;
	// 连续 sameConvMax 轮只拿到预览(preview_only)时,用最后一轮的 sediment 作为 IMG1 返回。
	// 协议/网络级错误(非 preview_only)会直接退出,由外层 Run 换账号。
	const sameConvMax = 3

	var (
		fileRefs        []string
		lastPreviewFids []string
		lastPreviewSids []string
		previewRounds   int
		// baselineTools 记录上一轮结束时会话里已有的 image_gen tool 消息 id,
		// 下一轮 PollConversationForImages 只看新增,避免把旧 preview 当本轮结果。
		baselineTools = map[string]struct{}{}
	)

loop:
	for turn := 1; turn <= sameConvMax; turn++ {
		result.TurnsInConv = turn

		// 每轮重新拉 chat_token + proof_token(之前那张已经消耗过)。
		// 复用外层 cr / proofToken 的首次结果(turn==1 直接用),后续重取。
		if turn > 1 {
			cr, err = cli.ChatRequirementsV2(ctx)
			if err != nil {
				return false, r.classifyUpstream(err), err
			}
			proofToken = ""
			if cr.Proofofwork.Required {
				proofCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
				ch := make(chan string, 1)
				go func() { ch <- cr.SolveProof(chatgpt.DefaultUserAgent) }()
				select {
				case <-proofCtx.Done():
					cancel()
					r.sched.MarkWarned(context.Background(), lease.Account.ID)
					return false, ErrPOWTimeout, proofCtx.Err()
				case proofToken = <-ch:
					cancel()
				}
				if proofToken == "" {
					r.sched.MarkWarned(context.Background(), lease.Account.ID)
					return false, ErrPOWFailed, errors.New("pow solver returned empty")
				}
			}
		}

		convOpt := chatgpt.ImageConvOpts{
			Prompt:        opt.Request.Prompt,
			UpstreamModel: upstreamModel,
			ConvID:        convID, // 第 1 轮空串=新会话,后续轮复用
			ParentMsgID:   parentID,
			MessageID:     messageID,
			ChatToken:     cr.Token,
			ProofToken:    proofToken,
			References:    refs,
		}
		if turn > 1 {
			// 续聊:每轮用新的 message_id,parent 来自上轮会话头
			convOpt.MessageID = uuid.NewString()
		}

		// Prepare(conduit_token;不成功也能降级跑 conversation)
		if ct, err := cli.PrepareFConversation(ctx, convOpt); err == nil {
			convOpt.ConduitToken = ct
		} else if ue, ok := err.(*chatgpt.UpstreamError); ok && ue.IsRateLimited() {
			r.sched.MarkRateLimited(context.Background(), lease.Account.ID)
			return false, ErrRateLimited, err
		}

		// f/conversation SSE
		stream, err := cli.StreamFConversation(ctx, convOpt)
		if err != nil {
			code := r.classifyUpstream(err)
			if code == ErrRateLimited {
				r.sched.MarkRateLimited(context.Background(), lease.Account.ID)
			}
			return false, code, err
		}
		sseResult := chatgpt.ParseImageSSE(stream)
		if sseResult.ConversationID != "" {
			convID = sseResult.ConversationID
			result.ConversationID = convID
		}

		// 每轮 SSE 解析完的原始产物:FileIDs(file-service://,IMG2 直出时有)、
		// SedimentIDs(sediment://,preview 或 IMG1 常见)、FinishType。用于诊断
		// "这轮到底返回了什么"。
		logger.L().Info("image runner SSE parsed",
			zap.String("task_id", opt.TaskID),
			zap.Uint64("account_id", lease.Account.ID),
			zap.Int("turn", turn),
			zap.String("conv_id", convID),
			zap.String("finish_type", sseResult.FinishType),
			zap.String("image_gen_task_id", sseResult.ImageGenTaskID),
			zap.Int("sse_fids", len(sseResult.FileIDs)),
			zap.Strings("sse_fids_list", sseResult.FileIDs),
			zap.Int("sse_sids", len(sseResult.SedimentIDs)),
			zap.Strings("sse_sids_list", sseResult.SedimentIDs),
		)

		// SSE 直出 file-service = IMG2 命中。
		// 注意:同一次灰度也可能同时带 sediment(例如 preview + final 各一张),
		// 都要收进来,不然用户会少看到图。
		if len(sseResult.FileIDs) > 0 {
			fileRefs = append(fileRefs, sseResult.FileIDs...)
			for _, s := range sseResult.SedimentIDs {
				fileRefs = append(fileRefs, "sed:"+s)
			}
			logger.L().Info("image runner IMG2 direct hit (from SSE)",
				zap.String("task_id", opt.TaskID),
				zap.Uint64("account_id", lease.Account.ID),
				zap.Int("turn", turn),
				zap.String("conv_id", convID),
				zap.Int("total_refs", len(fileRefs)),
				zap.Strings("refs", fileRefs),
			)
			break loop
		}

		// 没直出就轮询当前会话
		pollOpt := chatgpt.PollOpts{
			MaxWait:         opt.PollMaxWait,
			BaselineToolIDs: baselineTools,
		}
		status, fids, sids := cli.PollConversationForImages(ctx, convID, pollOpt)
		// 每轮 Poll 的产物,无论 status 如何都打印一条,用于诊断"第几轮拿到了什么"。
		logger.L().Info("image runner poll done",
			zap.String("task_id", opt.TaskID),
			zap.Uint64("account_id", lease.Account.ID),
			zap.Int("turn", turn),
			zap.String("conv_id", convID),
			zap.String("poll_status", string(status)),
			zap.Int("poll_fids", len(fids)),
			zap.Strings("poll_fids_list", fids),
			zap.Int("poll_sids", len(sids)),
			zap.Strings("poll_sids_list", sids),
		)
		switch status {
		case chatgpt.PollStatusIMG2:
			fileRefs = append(fileRefs, fids...)
			for _, s := range sids {
				fileRefs = append(fileRefs, "sed:"+s)
			}
			logger.L().Info("image runner IMG2 poll hit",
				zap.String("task_id", opt.TaskID),
				zap.Uint64("account_id", lease.Account.ID),
				zap.Int("turn", turn),
				zap.String("conv_id", convID),
				zap.Int("total_refs", len(fileRefs)),
				zap.Strings("refs", fileRefs),
			)
			break loop

		case chatgpt.PollStatusPreviewOnly:
			previewRounds++
			// 保留最新一轮的预览结果(3 轮都 preview 时作为 IMG1 兜底)
			lastPreviewFids = fids
			lastPreviewSids = sids
			logger.L().Info("image runner preview_only, retry in same conversation",
				zap.String("task_id", opt.TaskID),
				zap.Uint64("account_id", lease.Account.ID),
				zap.String("conv_id", convID),
				zap.Int("turn", turn),
				zap.Int("max_turns", sameConvMax),
				zap.Int("preview_fids", len(fids)),
				zap.Strings("preview_fids_list", fids),
				zap.Int("preview_sids", len(sids)),
				zap.Strings("preview_sids_list", sids),
			)

			// 不是最后一轮:更新 baseline + 取会话头作为下轮的 parent_message_id
			if turn < sameConvMax {
				if mapping, merr := cli.GetConversationMapping(ctx, convID); merr == nil {
					// 把当前所有 tool 消息都塞进 baseline,下轮 poll 只看新增
					if newBL := buildToolBaseline(mapping); newBL != nil {
						baselineTools = newBL
					}
					if head, _ := mapping["current_node"].(string); head != "" {
						parentID = head
					}
				} else {
					logger.L().Warn("image runner fetch mapping for retry failed",
						zap.Uint64("account_id", lease.Account.ID), zap.Error(merr))
				}
			}

		case chatgpt.PollStatusTimeout:
			return false, ErrPollTimeout, errors.New("poll timeout")

		default:
			return false, ErrUpstream, errors.New("poll error")
		}
	}

	// 若循环结束仍未拿到 IMG2,用最后一轮预览做 IMG1 兜底
	if len(fileRefs) == 0 {
		if len(lastPreviewFids) == 0 && len(lastPreviewSids) == 0 {
			return false, ErrPreviewOnly, errors.New("no image ref produced")
		}
		result.IsPreview = true
		fileRefs = append(fileRefs, lastPreviewFids...)
		for _, s := range lastPreviewSids {
			fileRefs = append(fileRefs, "sed:"+s)
		}
		logger.L().Warn("image runner fallback to IMG1 preview after exhausting retries",
			zap.String("task_id", opt.TaskID),
			zap.Uint64("account_id", lease.Account.ID),
			zap.String("conv_id", convID),
			zap.Int("preview_rounds", previewRounds),
			zap.Int("fids", len(lastPreviewFids)),
			zap.Int("sids", len(lastPreviewSids)))
	}

	// 8) 对每个 ref 取签名 URL
	var signedURLs []string
	var contentTypes []string
	for _, ref := range fileRefs {
		url, err := cli.ImageDownloadURL(ctx, convID, ref)
		if err != nil {
			logger.L().Warn("image runner download url failed",
				zap.String("ref", ref), zap.Error(err))
			continue
		}
		signedURLs = append(signedURLs, url)
		contentTypes = append(contentTypes, "image/png")
	}
	if len(signedURLs) == 0 {
		return false, ErrDownload, errors.New("all download urls failed")
	}

	// 最终汇总:把"这次任务跑完,真正给用户的图"用一条 Info 打出来。
	// 字段含义:
	//   is_preview   —— 是否 IMG1 预览兜底(3 轮都 preview_only);false 表示 IMG2 或 SSE 直出
	//   turns_used   —— 实际跑了几轮 f/conversation
	//   refs         —— 最终采用的所有 file ref(sed: 前缀 = sediment,否则 file-service id)
	//   signed_count —— 成功拿到签名 URL 的个数(可能 < len(refs) 如果部分 download 失败)
	logger.L().Info("image runner result summary",
		zap.String("task_id", opt.TaskID),
		zap.Uint64("account_id", lease.Account.ID),
		zap.String("conv_id", convID),
		zap.Int("turns_used", result.TurnsInConv),
		zap.Bool("is_preview", result.IsPreview),
		zap.Int("refs", len(fileRefs)),
		zap.Strings("refs_list", fileRefs),
		zap.Int("signed_count", len(signedURLs)),
	)

	result.FileIDs = fileRefs
	result.SignedURLs = signedURLs
	result.ContentTypes = contentTypes
	return true, "", nil
}

// buildToolBaseline 从 conversation mapping 里提取所有已存在的 image_gen tool 消息 id,
// 作为下一轮 PollConversationForImages 的 baseline。
func buildToolBaseline(mapping map[string]interface{}) map[string]struct{} {
	tools := chatgpt.ExtractImageToolMsgs(mapping)
	if len(tools) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(tools))
	for _, t := range tools {
		out[t.MessageID] = struct{}{}
	}
	return out
}

// classifyUpstream 把上游错误转成内部 error code。
func (r *Runner) classifyUpstream(err error) string {
	if err == nil {
		return ""
	}
	var ue *chatgpt.UpstreamError
	if errors.As(err, &ue) {
		if ue.IsRateLimited() {
			return ErrRateLimited
		}
		if ue.IsUnauthorized() {
			return ErrAuthRequired
		}
		return ErrUpstream
	}
	if strings.Contains(err.Error(), "deadline exceeded") {
		return ErrPollTimeout
	}
	return ErrUpstream
}

// GenerateTaskID 生成对外 task_id。
func GenerateTaskID() string {
	return "img_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:24]
}
