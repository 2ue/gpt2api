// Package recharge 负责充值套餐管理 + 下单 + 支付回调 + 入账 的全流程。
//
// 核心不变式:
//  1. 订单从 pending -> (paid|expired|cancelled|failed),状态只前进,不倒退。
//  2. paid 的订单**只能由经过签名校验的异步回调或管理员应急入账**触发,且整个入账
//     (UPDATE 订单 + INSERT credit_log + UPDATE users.credit_balance) 必须包在同一个事务里,
//     保证不会"钱到了但积分没加"。
//     对于支付平台已经确认成功的订单,即使本地状态先变成 cancelled/expired,也必须继续原子入账,
//     避免静默吞单。
//  3. 同一订单号的回调**幂等**:重复回调只返回 success,不再入账。
//  4. 签名校验失败的回调直接吞掉,且记录 warn 日志,不要暴露详情给上游。
package recharge

import "time"

// OrderStatus 订单状态。
const (
	StatusPending   = "pending"
	StatusPaid      = "paid"
	StatusExpired   = "expired"
	StatusCancelled = "cancelled"
	StatusFailed    = "failed"
)

// ChannelEPay 是目前唯一支持的支付通道标识。
// 预留多通道扩展字段,未来加微信/支付宝原生时只需在 pkg/ePay 同级目录加 adapter。
const (
	ChannelEPay = "epay"
)

// Package 对应 recharge_packages 表。
type Package struct {
	ID          uint64    `db:"id" json:"id"`
	Name        string    `db:"name" json:"name"`
	PriceCNY    int       `db:"price_cny" json:"price_cny"` // 分
	Credits     int64     `db:"credits" json:"credits"`     // 厘
	Bonus       int64     `db:"bonus" json:"bonus"`         // 厘
	Description string    `db:"description" json:"description"`
	Sort        int       `db:"sort" json:"sort"`
	Enabled     bool      `db:"enabled" json:"enabled"`
	CreatedAt   time.Time `db:"created_at" json:"created_at"`
	UpdatedAt   time.Time `db:"updated_at" json:"updated_at"`
}

// Order 对应 recharge_orders 表。
// NotifyRaw / Remark 等非核心字段对用户 API 返回时会被剥掉。
type Order struct {
	ID         uint64     `db:"id" json:"id"`
	OutTradeNo string     `db:"out_trade_no" json:"out_trade_no"`
	UserID     uint64     `db:"user_id" json:"user_id"`
	PackageID  uint64     `db:"package_id" json:"package_id"`
	PriceCNY   int        `db:"price_cny" json:"price_cny"`
	Credits    int64      `db:"credits" json:"credits"`
	Bonus      int64      `db:"bonus" json:"bonus"`
	Channel    string     `db:"channel" json:"channel"`
	PayMethod  string     `db:"pay_method" json:"pay_method"`
	Status     string     `db:"status" json:"status"`
	TradeNo    string     `db:"trade_no" json:"trade_no"`
	PaidAt     *time.Time `db:"paid_at" json:"paid_at,omitempty"`
	PayURL     string     `db:"pay_url" json:"pay_url,omitempty"`
	ClientIP   string     `db:"client_ip" json:"-"`
	NotifyRaw  *string    `db:"notify_raw" json:"-"`
	Remark     string     `db:"remark" json:"remark,omitempty"`
	CreatedAt  time.Time  `db:"created_at" json:"created_at"`
	UpdatedAt  time.Time  `db:"updated_at" json:"updated_at"`
}

// TotalCredits 返回订单最终应到账的积分(基础 + 赠送)。
func (o *Order) TotalCredits() int64 { return o.Credits + o.Bonus }
