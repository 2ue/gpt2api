package account

import (
	"database/sql"
	"encoding/json"
	"time"
)

// 账号状态常量。
const (
	StatusHealthy    = "healthy"
	StatusWarned     = "warned"
	StatusThrottled  = "throttled"
	StatusSuspicious = "suspicious"
	StatusDead       = "dead"
)

// 刷新来源。
const (
	RefreshSourceRT     = "rt"
	RefreshSourceST     = "st"
	RefreshSourceManual = "manual"
)

// 图片 provider 类型。
const (
	ProviderKindReverse   = "reverse"
	ProviderKindNative    = "native"
	ProviderKindResponses = "responses_tool"
)

// Account 对应 oai_accounts 表。
type Account struct {
	ID                uint64         `db:"id" json:"id"`
	Email             string         `db:"email" json:"email"`
	AuthTokenEnc      string         `db:"auth_token_enc" json:"-"`
	RefreshTokenEnc   sql.NullString `db:"refresh_token_enc" json:"-"`
	SessionTokenEnc   sql.NullString `db:"session_token_enc" json:"-"`
	APIKeyEnc         sql.NullString `db:"api_key_enc" json:"-"`
	TokenExpiresAt    sql.NullTime   `db:"token_expires_at" json:"token_expires_at,omitempty"`
	OAISessionID      string         `db:"oai_session_id" json:"oai_session_id"`
	OAIDeviceID       string         `db:"oai_device_id" json:"oai_device_id"`
	ClientID          string         `db:"client_id" json:"client_id"`
	APIBaseURL        string         `db:"api_base_url" json:"api_base_url"`
	ProviderKind      string         `db:"provider_kind" json:"provider_kind"`
	ImageCapabilities []byte         `db:"image_capabilities_json" json:"image_capabilities_json,omitempty"`
	SameAccountRetryLimit int        `db:"same_account_retry_limit" json:"same_account_retry_limit"`
	Priority          int            `db:"priority" json:"priority"`
	ChatGPTAccountID  string         `db:"chatgpt_account_id" json:"chatgpt_account_id"`
	AccountType       string         `db:"account_type" json:"account_type"`
	PlanType          string         `db:"plan_type" json:"plan_type"`
	DailyImageQuota   int            `db:"daily_image_quota" json:"daily_image_quota"`
	Status            string         `db:"status" json:"status"`
	WarnedAt          sql.NullTime   `db:"warned_at" json:"warned_at,omitempty"`
	CooldownUntil     sql.NullTime   `db:"cooldown_until" json:"cooldown_until,omitempty"`
	LastUsedAt        sql.NullTime   `db:"last_used_at" json:"last_used_at,omitempty"`
	TodayUsedCount    int            `db:"today_used_count" json:"today_used_count"`
	TodayUsedDate     sql.NullTime   `db:"today_used_date" json:"today_used_date,omitempty"`

	LastRefreshAt      sql.NullTime `db:"last_refresh_at" json:"last_refresh_at,omitempty"`
	LastRefreshSource  string       `db:"last_refresh_source" json:"last_refresh_source"`
	RefreshError       string       `db:"refresh_error" json:"refresh_error"`

	ImageQuotaRemaining int          `db:"image_quota_remaining" json:"image_quota_remaining"`
	ImageQuotaTotal     int          `db:"image_quota_total"     json:"image_quota_total"`
	ImageQuotaResetAt   sql.NullTime `db:"image_quota_reset_at"   json:"image_quota_reset_at,omitempty"`
	ImageQuotaUpdatedAt sql.NullTime `db:"image_quota_updated_at" json:"image_quota_updated_at,omitempty"`

	Notes     string       `db:"notes" json:"notes"`
	CreatedAt time.Time    `db:"created_at" json:"created_at"`
	UpdatedAt time.Time    `db:"updated_at" json:"updated_at"`
	DeletedAt sql.NullTime `db:"deleted_at" json:"-"`

	// 辅助字段(非数据库列):前端展示用标志位。
	HasRT bool `db:"-" json:"has_rt"`
	HasST bool `db:"-" json:"has_st"`
	HasAPIKey bool `db:"-" json:"has_api_key"`
}

// ImageCapabilityMap 解出 capability flags。
func (a *Account) ImageCapabilityMap() map[string]bool {
	if a == nil || len(a.ImageCapabilities) == 0 {
		return nil
	}
	out := map[string]bool{}
	_ = json.Unmarshal(a.ImageCapabilities, &out)
	return out
}

// Binding 对应 account_proxy_bindings 表。
type Binding struct {
	AccountID uint64    `db:"account_id" json:"account_id"`
	ProxyID   uint64    `db:"proxy_id" json:"proxy_id"`
	BoundAt   time.Time `db:"bound_at" json:"bound_at"`
}
