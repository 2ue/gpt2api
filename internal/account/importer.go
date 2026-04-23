package account

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// ImportSource 代表一条待导入记录,来自任意一种 JSON 格式。
type ImportSource struct {
	// 必填
	AccessToken string
	Email       string

	// 可选
	RefreshToken     string
	SessionToken     string // 从 cookie 里提取的 __Secure-next-auth.session-token
	ClientID         string
	ChatGPTAccountID string
	AccountType      string // codex / chatgpt
	ExpiredAt        time.Time
	Name             string // sub2api 里的 name 字段(当 email 缺失时退化为 email)
}

// ImportLineResult 返回给前端,每条记录处理结果。
type ImportLineResult struct {
	Index  int    `json:"index"`
	Email  string `json:"email"`
	Status string `json:"status"` // created / updated / skipped / failed
	Reason string `json:"reason,omitempty"`
	ID     uint64 `json:"id,omitempty"`
}

// ImportSummary 整体统计。
type ImportSummary struct {
	Total   int                `json:"total"`
	Created int                `json:"created"`
	Updated int                `json:"updated"`
	Skipped int                `json:"skipped"`
	Failed  int                `json:"failed"`
	Results []ImportLineResult `json:"results"`
}

// ImportOptions 批量导入选项。
type ImportOptions struct {
	// UpdateExisting 为 true 时 email 已存在则更新 token;false 则 skipped。
	UpdateExisting bool
	// DefaultClientID 当记录里没有 client_id 时填充的值。
	DefaultClientID string
	// DefaultProxyID 新建账号时默认绑定的代理 id(0 = 不绑)。
	DefaultProxyID uint64
	// BatchSize 分批 commit 的大小(仅用于让出 CPU,每批做一次 context check)。默认 200。
	BatchSize int
}

// ParsedImportBlob 是 ParseJSONBlobDetailed 的解析结果。
type ParsedImportBlob struct {
	Sources []ImportSource
	Skipped []ImportLineResult
}

// ParseJSONBlob 尝试把用户上传的文本解析成 ImportSource 列表。
// 同时兼容以下输入:
//  1. 顶层是对象且含 `accounts` 数组 → sub2api 多账号导出
//  2. 顶层是对象且含 `access_token` / `accessToken` → 单账号 token_xxx.json
//  3. 顶层是数组,每个元素同 (1)/(2) 的单个对象
//  4. 多个 JSON 文本用换行/空行分隔(JSONL)
func ParseJSONBlob(raw string) ([]ImportSource, error) {
	parsed, err := ParseJSONBlobDetailed(raw)
	if err != nil {
		return nil, err
	}
	return parsed.Sources, nil
}

// ParseJSONBlobDetailed 会额外返回 sub2api 中不支持导入的 skipped 明细。
func ParseJSONBlobDetailed(raw string) (*ParsedImportBlob, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("输入为空")
	}
	// 先尝试整体解析
	if parsed, matched, err := parseSingleJSONDetailed(raw); err == nil && matched {
		return parsed, nil
	}
	// 再尝试 JSONL
	all := &ParsedImportBlob{}
	var firstErr error
	var decodeErr error
	matchedAny := false
	dec := json.NewDecoder(strings.NewReader(raw))
	for {
		var one json.RawMessage
		if err := dec.Decode(&one); err != nil {
			if err == io.EOF {
				break
			}
			decodeErr = err
			break
		}
		parsed, matched, err := parseSingleJSONDetailed(string(one))
		if err != nil || !matched {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		matchedAny = true
		all.Sources = append(all.Sources, parsed.Sources...)
		all.Skipped = append(all.Skipped, parsed.Skipped...)
	}
	if decodeErr != nil {
		return nil, decodeErr
	}
	if !matchedAny {
		if firstErr == nil {
			firstErr = errors.New("无法识别的 JSON 格式")
		}
		return nil, firstErr
	}
	return all, nil
}

func parseSingleJSONDetailed(s string) (*ParsedImportBlob, bool, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, false, errors.New("空 JSON")
	}
	// 1) 数组
	if s[0] == '[' {
		var arr []json.RawMessage
		if err := json.Unmarshal([]byte(s), &arr); err != nil {
			return nil, false, fmt.Errorf("解析 JSON 数组失败:%w", err)
		}
		all := &ParsedImportBlob{}
		if len(arr) == 0 {
			return all, true, nil
		}
		matchedAny := false
		var firstErr error
		for _, item := range arr {
			parsed, matched, err := parseSingleJSONDetailed(string(item))
			if err != nil || !matched {
				if firstErr == nil && err != nil {
					firstErr = err
				}
				continue
			}
			matchedAny = true
			all.Sources = append(all.Sources, parsed.Sources...)
			all.Skipped = append(all.Skipped, parsed.Skipped...)
		}
		if !matchedAny {
			if firstErr == nil {
				firstErr = errors.New("未识别的 JSON 数组元素")
			}
			return nil, false, firstErr
		}
		return all, true, nil
	}
	// 2) 对象
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(s), &obj); err != nil {
		return nil, false, fmt.Errorf("解析 JSON 对象失败:%w", err)
	}

	// Format A: 有 accounts 数组
	if v, ok := obj["accounts"]; ok {
		var accs []subAPIAccount
		if err := json.Unmarshal(v, &accs); err == nil {
			out := &ParsedImportBlob{
				Sources: make([]ImportSource, 0, len(accs)),
				Skipped: make([]ImportLineResult, 0, len(accs)),
			}
			for idx, a := range accs {
				src, reason := a.importableStatus()
				if reason == "" {
					out.Sources = append(out.Sources, src)
					continue
				}
				email := strings.TrimSpace(src.Email)
				if email == "" {
					email = strings.TrimSpace(a.derivedEmail())
				}
				if email == "" {
					email = strings.TrimSpace(a.Name)
				}
				out.Skipped = append(out.Skipped, ImportLineResult{
					Index:  idx,
					Email:  email,
					Status: "skipped",
					Reason: reason,
				})
			}
			return out, true, nil
		}
	}

	// Format B: 单账号 token_xxx.json(扁平对象)
	if _, has := obj["access_token"]; has {
		var b tokenFileB
		if err := json.Unmarshal([]byte(s), &b); err != nil {
			return nil, true, fmt.Errorf("解析 token 文件失败:%w", err)
		}
		src, ok := b.toSource()
		if !ok {
			return nil, true, errors.New("token 文件缺少必要字段")
		}
		return &ParsedImportBlob{Sources: []ImportSource{src}}, true, nil
	}
	// 兼容 accessToken(驼峰)
	if _, has := obj["accessToken"]; has {
		var b tokenFileB
		// 同名 camelCase 字段也走 tokenFileB;json tag 都用 snake_case
		// 这里用一个临时结构
		var camel struct {
			AccessToken  string `json:"accessToken"`
			RefreshToken string `json:"refreshToken"`
			Email        string `json:"email"`
			AccountID    string `json:"account_id"`
			Type         string `json:"type"`
			ClientID     string `json:"client_id"`
			Expired      string `json:"expires"`
		}
		if err := json.Unmarshal([]byte(s), &camel); err != nil {
			return nil, true, err
		}
		b.AccessToken = camel.AccessToken
		b.RefreshToken = camel.RefreshToken
		b.Email = camel.Email
		b.AccountID = camel.AccountID
		b.Type = camel.Type
		b.ClientID = camel.ClientID
		b.Expired = camel.Expired
		src, ok := b.toSource()
		if !ok {
			return nil, true, errors.New("token 文件缺少必要字段")
		}
		return &ParsedImportBlob{Sources: []ImportSource{src}}, true, nil
	}

	return nil, false, errors.New("未识别的 JSON 结构(既不是 sub2api 也不是 token 文件)")
}

func mergeImportSummaryWithSkipped(summary *ImportSummary, skipped []ImportLineResult) *ImportSummary {
	if summary == nil {
		summary = &ImportSummary{}
	}
	if len(summary.Results) == 0 {
		summary.Results = []ImportLineResult{}
	}
	if len(skipped) == 0 {
		return summary
	}

	merged := &ImportSummary{
		Total:   summary.Total + len(skipped),
		Created: summary.Created,
		Updated: summary.Updated,
		Skipped: summary.Skipped + len(skipped),
		Failed:  summary.Failed,
		Results: make([]ImportLineResult, 0, len(skipped)+len(summary.Results)),
	}
	merged.Results = append(merged.Results, skipped...)
	merged.Results = append(merged.Results, summary.Results...)
	for idx := range merged.Results {
		merged.Results[idx].Index = idx
	}
	merged.Total = len(merged.Results)
	return merged
}

// sub2api 的 account 结构片段。
type subAPIAccount struct {
	Name        string `json:"name"`
	Platform    string `json:"platform"`
	Type        string `json:"type"`
	Credentials struct {
		AccessToken      string `json:"access_token"`
		RefreshToken     string `json:"refresh_token"`
		SessionToken     string `json:"session_token"`
		ClientID         string `json:"client_id"`
		ChatGPTAccountID string `json:"chatgpt_account_id"`
		Email            string `json:"email"`
	} `json:"credentials"`
	Extra struct {
		Email        string `json:"email"`
		EmailAddress string `json:"email_address"`
	} `json:"extra"`
}

func (a subAPIAccount) toSource() (ImportSource, bool) {
	src, reason := a.importableStatus()
	return src, reason == ""
}

func (a subAPIAccount) importableStatus() (ImportSource, string) {
	email := a.derivedEmail()
	accountID := strings.TrimSpace(a.Credentials.ChatGPTAccountID)
	expAt := time.Time{}
	if a.Credentials.AccessToken != "" {
		if atEmail, atAccountID, atExpAt, err := decodeATClaims(a.Credentials.AccessToken); err == nil {
			if email == "" {
				email = strings.TrimSpace(atEmail)
			}
			if accountID == "" {
				accountID = strings.TrimSpace(atAccountID)
			}
			if expAt.IsZero() {
				expAt = atExpAt
			}
		}
	}
	if email == "" {
		email = emailFromName(a.Name)
	}

	src := ImportSource{
		AccessToken:      a.Credentials.AccessToken,
		RefreshToken:     a.Credentials.RefreshToken,
		SessionToken:     a.Credentials.SessionToken,
		ClientID:         a.Credentials.ClientID,
		ChatGPTAccountID: accountID,
		AccountType:      normalizeType(a.Name, a.Platform),
		Email:            email,
		Name:             a.Name,
		ExpiredAt:        expAt,
	}

	platform := strings.ToLower(strings.TrimSpace(a.Platform))
	if platform != "" && platform != "openai" {
		return src, "仅支持 OpenAI 账号"
	}
	if src.AccessToken == "" {
		return src, "缺少 access_token"
	}
	if src.Email == "" {
		return src, "缺少 email"
	}
	return src, ""
}

func (a subAPIAccount) derivedEmail() string {
	for _, candidate := range []string{a.Extra.Email, a.Extra.EmailAddress, a.Credentials.Email} {
		if value := strings.TrimSpace(candidate); value != "" {
			return value
		}
	}
	if a.Credentials.AccessToken != "" {
		if email, _, _, err := decodeATClaims(a.Credentials.AccessToken); err == nil {
			if value := strings.TrimSpace(email); value != "" {
				return value
			}
		}
	}
	if value := emailFromName(a.Name); value != "" {
		return value
	}
	return ""
}

// tokenFileB 对应 token_xxx.json。
type tokenFileB struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	AccountID    string `json:"account_id"`
	Email        string `json:"email"`
	Type         string `json:"type"`
	ClientID     string `json:"client_id"`
	Expired      string `json:"expired"`
}

func (b tokenFileB) toSource() (ImportSource, bool) {
	src := ImportSource{
		AccessToken:      b.AccessToken,
		RefreshToken:     b.RefreshToken,
		Email:            strings.TrimSpace(b.Email),
		ChatGPTAccountID: b.AccountID,
		ClientID:         b.ClientID,
		AccountType:      strings.ToLower(strings.TrimSpace(b.Type)),
	}
	if src.AccountType == "" {
		src.AccountType = "codex"
	}
	if b.Expired != "" {
		if t, err := time.Parse(time.RFC3339, b.Expired); err == nil {
			src.ExpiredAt = t
		}
	}
	if src.AccessToken == "" || src.Email == "" {
		return src, false
	}
	return src, true
}

// emailFromName 把 sub2api 的 name (codex-user_hotmail.com) 反推成 email。
func emailFromName(name string) string {
	if name == "" {
		return ""
	}
	n := name
	for _, prefix := range []string{"codex-", "chatgpt-", "openai-"} {
		if strings.HasPrefix(n, prefix) {
			n = strings.TrimPrefix(n, prefix)
			break
		}
	}
	// 最后一个 _ 之前视为 localpart,之后视为 domain
	idx := strings.LastIndex(n, "_")
	if idx > 0 && idx < len(n)-1 {
		return n[:idx] + "@" + n[idx+1:]
	}
	return ""
}

func normalizeType(name, platform string) string {
	lower := strings.ToLower(name + " " + platform)
	switch {
	case strings.Contains(lower, "codex"):
		return "codex"
	case strings.Contains(lower, "chatgpt"):
		return "chatgpt"
	case strings.Contains(lower, "openai"):
		return "codex"
	default:
		return "codex"
	}
}

// ImportBatch 执行批量导入。
// 处理策略:
//   - 同一批内 email 去重(后者覆盖前者)
//   - email 已存在则按 UpdateExisting 决定更新或 skip
//   - 每 BatchSize 条让出一次 CPU,并检查 ctx.Done(),便于大批量
//   - 不做整体事务(失败项不影响成功项);单条失败只影响该条
func (s *Service) ImportBatch(ctx context.Context, items []ImportSource, opt ImportOptions) *ImportSummary {
	if opt.BatchSize <= 0 {
		opt.BatchSize = 200
	}
	if opt.DefaultClientID == "" {
		opt.DefaultClientID = "app_EMoamEEZ73f0CkXaXp7hrann"
	}

	// email 去重(后者覆盖)
	seen := make(map[string]int, len(items))
	dedup := make([]ImportSource, 0, len(items))
	for _, it := range items {
		if it.Email == "" {
			continue
		}
		key := strings.ToLower(it.Email)
		if idx, ok := seen[key]; ok {
			dedup[idx] = it // 覆盖
		} else {
			seen[key] = len(dedup)
			dedup = append(dedup, it)
		}
	}

	sum := &ImportSummary{
		Total:   len(dedup),
		Results: make([]ImportLineResult, 0, len(dedup)),
	}

	for i, it := range dedup {
		if i > 0 && i%opt.BatchSize == 0 {
			// 让出一次 CPU;大批量下防止长时间独占
			select {
			case <-ctx.Done():
				sum.Failed += len(dedup) - i
				sum.Results = append(sum.Results, ImportLineResult{
					Index: i, Email: "", Status: "failed", Reason: "导入被取消",
				})
				return sum
			default:
			}
		}
		res := s.importOne(ctx, i, it, opt)
		switch res.Status {
		case "created":
			sum.Created++
		case "updated":
			sum.Updated++
		case "skipped":
			sum.Skipped++
		case "failed":
			sum.Failed++
		}
		sum.Results = append(sum.Results, res)
	}
	return sum
}

func (s *Service) importOne(ctx context.Context, idx int, it ImportSource, opt ImportOptions) ImportLineResult {
	out := ImportLineResult{Index: idx, Email: it.Email}

	if it.AccessToken == "" {
		out.Status = "failed"
		out.Reason = "缺少 access_token"
		return out
	}

	// 计算过期时间:优先用 JSON 的 expired,其次解析 JWT
	expAt := it.ExpiredAt
	if expAt.IsZero() {
		expAt = parseJWTExp(it.AccessToken)
	}

	clientID := it.ClientID
	if clientID == "" {
		clientID = opt.DefaultClientID
	}
	accountType := it.AccountType
	if accountType == "" {
		accountType = "codex"
	}

	// 查是否已存在
	existing, err := s.dao.GetByEmail(ctx, it.Email)
	if err != nil {
		out.Status = "failed"
		out.Reason = "查询失败:" + err.Error()
		return out
	}

	atEnc, err := s.cipher.EncryptString(it.AccessToken)
	if err != nil {
		out.Status = "failed"
		out.Reason = "AT 加密失败:" + err.Error()
		return out
	}
	var rtEnc, stEnc string
	if it.RefreshToken != "" {
		if v, err := s.cipher.EncryptString(it.RefreshToken); err == nil {
			rtEnc = v
		}
	}
	if it.SessionToken != "" {
		if v, err := s.cipher.EncryptString(it.SessionToken); err == nil {
			stEnc = v
		}
	}

	if existing == nil {
		// 新建
		a := &Account{
			Email:            it.Email,
			AuthTokenEnc:     atEnc,
			ClientID:         clientID,
			ChatGPTAccountID: it.ChatGPTAccountID,
			AccountType:      accountType,
			PlanType:         "free",
			DailyImageQuota:  100,
			Status:           StatusHealthy,
		}
		if rtEnc != "" {
			a.RefreshTokenEnc.String = rtEnc
			a.RefreshTokenEnc.Valid = true
		}
		if stEnc != "" {
			a.SessionTokenEnc.String = stEnc
			a.SessionTokenEnc.Valid = true
		}
		if !expAt.IsZero() {
			a.TokenExpiresAt.Time = expAt
			a.TokenExpiresAt.Valid = true
		}
		id, err := s.dao.Create(ctx, a)
		if err != nil {
			out.Status = "failed"
			out.Reason = "入库失败:" + err.Error()
			return out
		}
		if opt.DefaultProxyID > 0 {
			_ = s.dao.SetBinding(ctx, id, opt.DefaultProxyID)
		}
		out.Status = "created"
		out.ID = id
		return out
	}

	// 已存在
	if !opt.UpdateExisting {
		out.Status = "skipped"
		out.Reason = "邮箱已存在"
		out.ID = existing.ID
		return out
	}
	// 更新 token 字段,其它字段保持
	existing.AuthTokenEnc = atEnc
	if rtEnc != "" {
		existing.RefreshTokenEnc.String = rtEnc
		existing.RefreshTokenEnc.Valid = true
	}
	if stEnc != "" {
		existing.SessionTokenEnc.String = stEnc
		existing.SessionTokenEnc.Valid = true
	}
	if clientID != "" {
		existing.ClientID = clientID
	}
	if it.ChatGPTAccountID != "" {
		existing.ChatGPTAccountID = it.ChatGPTAccountID
	}
	if accountType != "" {
		existing.AccountType = accountType
	}
	if !expAt.IsZero() {
		existing.TokenExpiresAt.Time = expAt
		existing.TokenExpiresAt.Valid = true
	}
	// 复活已死账号(导入新 token 视为重新投放)
	if existing.Status == StatusDead || existing.Status == StatusSuspicious {
		existing.Status = StatusHealthy
	}
	if err := s.dao.Update(ctx, existing); err != nil {
		out.Status = "failed"
		out.Reason = "更新失败:" + err.Error()
		return out
	}
	out.Status = "updated"
	out.ID = existing.ID
	return out
}
