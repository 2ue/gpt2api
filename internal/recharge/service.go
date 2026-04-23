package recharge

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"

	"github.com/jmoiron/sqlx"
	"go.uber.org/zap"

	"github.com/432539/gpt2api/internal/billing"
	"github.com/432539/gpt2api/internal/config"
	"github.com/432539/gpt2api/internal/settings"
	"github.com/432539/gpt2api/internal/user"
	"github.com/432539/gpt2api/pkg/epay"
	"github.com/432539/gpt2api/pkg/mailer"
)

var (
	ErrPackageUnavailable = errors.New("recharge: package not available")
	ErrChannelDisabled    = errors.New("recharge: pay channel disabled")
	ErrOrderNotFound      = errors.New("recharge: order not found")
	ErrOrderStateInvalid  = errors.New("recharge: order state invalid")
	ErrRechargeDisabled   = errors.New("recharge: recharge is disabled by admin")
	ErrAmountOutOfRange   = errors.New("recharge: amount out of allowed range")
	ErrDailyLimitExceeded = errors.New("recharge: daily limit exceeded")
)

// Service 协调下单、回调入账、查询。
type Service struct {
	dao     *DAO
	billing *billing.Engine
	users   *user.DAO
	signer  *epay.Signer
	cfg     config.EPayConfig
	mail    *mailer.Mailer
	baseURL string // app.base_url 用于邮件里的链接
	log     *zap.Logger

	// settings 可为 nil(兼容旧调用方);为 nil 时使用硬编码兜底
	settings *settings.Service
}

// SetSettings 注入系统设置,用于下单时的开关/金额/日上限/过期分钟。
func (s *Service) SetSettings(ss *settings.Service) { s.settings = ss }

// NewService 构造 Service。
// ePayCfg.GatewayURL 为空时 Service.Enabled()==false,所有下单请求会被拒绝。
func NewService(dao *DAO, bill *billing.Engine, users *user.DAO,
	ePayCfg config.EPayConfig, mail *mailer.Mailer, baseURL string, log *zap.Logger,
) *Service {
	return &Service{
		dao:     dao,
		billing: bill,
		users:   users,
		signer:  epay.NewSigner(ePayCfg.PID, ePayCfg.Key, ePayCfg.SignType),
		cfg:     ePayCfg,
		mail:    mail,
		baseURL: baseURL,
		log:     log.With(zap.String("mod", "recharge")),
	}
}

// Enabled 表示 epay 通道是否已配置完整(运维侧)。
func (s *Service) Enabled() bool {
	return s.cfg.GatewayURL != "" && s.cfg.PID != "" && s.cfg.Key != ""
}

// AdminEnabled 表示"管理员是否允许充值入口"(业务侧开关)。未注入 settings 视为允许。
func (s *Service) AdminEnabled() bool {
	if s.settings == nil {
		return true
	}
	return s.settings.RechargeEnabled()
}

func (s *Service) MinAmountCNY() int64 {
	if s.settings == nil {
		return 0
	}
	return s.settings.RechargeMinCNY()
}
func (s *Service) MaxAmountCNY() int64 {
	if s.settings == nil {
		return 0
	}
	return s.settings.RechargeMaxCNY()
}
func (s *Service) DailyLimitCNY() int64 {
	if s.settings == nil {
		return 0
	}
	return s.settings.RechargeDailyLimitCNY()
}
func (s *Service) OrderExpireMinutes() int {
	if s.settings == nil {
		return 30
	}
	return s.settings.RechargeOrderExpireMin()
}

// ---------- Package 读 ----------

func (s *Service) ListEnabledPackages(ctx context.Context) ([]Package, error) {
	return s.dao.ListPackages(ctx, true)
}

// ---------- 下单 ----------

// CreateInput 用户下单参数。
type CreateInput struct {
	UserID    uint64
	PackageID uint64
	// PayType 可选,决定 epay 网关跳出来默认哪种二维码。
	// "" 让收银台自选;常见值 "alipay" / "wxpay"。
	PayType  string
	ClientIP string
}

// Create 创建订单并生成跳转 URL。
func (s *Service) Create(ctx context.Context, in CreateInput) (*Order, error) {
	if !s.Enabled() {
		return nil, ErrChannelDisabled
	}
	// 充值总开关(settings 未注入时视为允许,兼容旧行为)
	if s.settings != nil && !s.settings.RechargeEnabled() {
		return nil, ErrRechargeDisabled
	}
	pkg, err := s.dao.GetPackage(ctx, in.PackageID)
	if err != nil {
		return nil, err
	}
	if !pkg.Enabled {
		return nil, ErrPackageUnavailable
	}

	// 金额范围(分)+ 单用户每日累计上限校验
	if s.settings != nil {
		price := int64(pkg.PriceCNY)
		if min := s.settings.RechargeMinCNY(); min > 0 && price < min {
			return nil, ErrAmountOutOfRange
		}
		if max := s.settings.RechargeMaxCNY(); max > 0 && price > max {
			return nil, ErrAmountOutOfRange
		}
	}

	outTradeNo := genTradeNo()
	extra := map[string]string{}
	if in.PayType != "" {
		extra["type"] = in.PayType
	}
	payURL, err := s.signer.BuildPayURL(
		s.cfg.GatewayURL, outTradeNo, pkg.Name,
		pkg.PriceCNY, s.cfg.NotifyURL, s.cfg.ReturnURL, extra,
	)
	if err != nil {
		return nil, err
	}

	o := &Order{
		OutTradeNo: outTradeNo,
		UserID:     in.UserID,
		PackageID:  pkg.ID,
		PriceCNY:   pkg.PriceCNY,
		Credits:    pkg.Credits,
		Bonus:      pkg.Bonus,
		Channel:    ChannelEPay,
		PayMethod:  in.PayType,
		Status:     StatusPending,
		PayURL:     payURL,
		ClientIP:   in.ClientIP,
		Remark:     pkg.Name,
	}
	if err := s.insertOrder(ctx, o); err != nil {
		return nil, err
	}
	return o, nil
}

// CancelByUser 用户主动取消 pending 订单。
// 已支付订单不允许取消。
func (s *Service) CancelByUser(ctx context.Context, userID, orderID uint64) error {
	o, err := s.dao.GetByID(ctx, orderID)
	if err != nil {
		return err
	}
	if o.UserID != userID {
		return ErrOrderNotFound // 越权一律按 not-found 处理,防枚举
	}
	if o.Status != StatusPending {
		return ErrOrderStateInvalid
	}
	_, err = s.dao.DB().ExecContext(ctx,
		`UPDATE recharge_orders SET status = ? WHERE id = ? AND status = ?`,
		StatusCancelled, orderID, StatusPending)
	return err
}

// ---------- 回调入账 ----------

// HandleNotify 异步回调处理。返回 (上游期望文本, error)。
//   - 上游期望文本:按 epay 规范,无论"成功/已处理"都必须回 "success";
//     只有完全没处理 / 有异常时才允许回其它内容,以触发上游重发。
//   - 我们出于幂等,收到一笔**已入账**的订单再次回调,也回 "success"。
func (s *Service) HandleNotify(ctx context.Context, form url.Values) (string, error) {
	pl, err := s.signer.ParseNotify(form)
	if err != nil {
		s.log.Warn("notify signature invalid",
			zap.String("out_trade_no", form.Get("out_trade_no")))
		return "fail", err
	}
	o, err := s.dao.GetByOutTradeNo(ctx, pl.OutTradeNo)
	if err != nil {
		s.log.Warn("notify order not found",
			zap.String("out_trade_no", pl.OutTradeNo))
		return "fail", err
	}

	// 幂等
	if o.Status == StatusPaid {
		return "success", nil
	}
	if pl.TradeStatus != "TRADE_SUCCESS" {
		// 上游可能先发一笔"等待付款"之类中间状态,这里简单回 success,后续覆盖。
		return "success", nil
	}

	// 金额二次校验:money 是 "元",priceCNY 是 "分"
	if err := verifyAmount(pl.Money, o.PriceCNY); err != nil {
		s.log.Warn("notify amount mismatch",
			zap.String("out_trade_no", pl.OutTradeNo),
			zap.String("got_money", pl.Money),
			zap.Int("want_fen", o.PriceCNY))
		return "fail", err
	}

	if err := s.settle(ctx, o, pl); err != nil {
		s.log.Error("notify settle failed",
			zap.String("out_trade_no", pl.OutTradeNo),
			zap.Error(err))
		return "fail", err
	}
	return "success", nil
}

// settle 单次入账:在同一个事务里更新订单为 paid 并增加积分。
// 对于支付平台已经确认成功的回调,即使订单先被取消/过期,也要继续入账以避免吞单。
func (s *Service) settle(ctx context.Context, o *Order, pl *epay.NotifyPayload) error {
	tx, err := s.dao.DB().BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	locked, err := loadOrderForUpdate(ctx, tx, o.ID)
	if err != nil {
		return err
	}
	if locked.Status == StatusPaid {
		return nil
	}
	if !notifyPayableStatus(locked.Status) {
		return ErrOrderStateInvalid
	}

	latePaid := locked.Status != StatusPending
	paidAt := nowUTC()
	if _, err := tx.ExecContext(ctx,
		`UPDATE recharge_orders
           SET status = ?, trade_no = ?, pay_method = ?, paid_at = ?, notify_raw = ?
         WHERE id = ?`,
		StatusPaid, pl.TradeNo, pl.Type, paidAt, rawDump(pl.Raw), locked.ID); err != nil {
		return err
	}

	refID := fmt.Sprintf("order:%s", locked.OutTradeNo)
	remark := fmt.Sprintf("充值:%s", locked.Remark)
	if err := s.billing.RechargeTx(ctx, tx, locked.UserID, locked.TotalCredits(), refID, remark); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	if latePaid {
		s.log.Warn("late paid order settled after local status changed",
			zap.String("out_trade_no", locked.OutTradeNo),
			zap.String("from_status", locked.Status),
			zap.String("trade_no", pl.TradeNo))
	}

	// 异步邮件通知(失败不影响主流程)
	if s.mail != nil && !s.mail.Disabled() {
		if u, err := s.users.GetByID(ctx, locked.UserID); err == nil && u.Email != "" {
			subject, html := mailer.RenderPaid(u.Nickname, locked.OutTradeNo, locked.PriceCNY, locked.Credits, locked.Bonus, paidAt)
			s.mail.Send(mailer.Message{To: u.Email, Subject: subject, HTML: html})
		}
	}
	return nil
}

// ---------- admin/ Package 写 ----------

func (s *Service) AdminCreatePackage(ctx context.Context, p *Package) (uint64, error) {
	return s.dao.CreatePackage(ctx, p)
}
func (s *Service) AdminUpdatePackage(ctx context.Context, p *Package) error {
	return s.dao.UpdatePackage(ctx, p)
}
func (s *Service) AdminDeletePackage(ctx context.Context, id uint64) error {
	return s.dao.DeletePackage(ctx, id)
}
func (s *Service) AdminListPackages(ctx context.Context) ([]Package, error) {
	return s.dao.ListPackages(ctx, false)
}

// ---------- Orders 读 ----------

func (s *Service) ListUserOrders(ctx context.Context, userID uint64, status string, offset, limit int) ([]Order, int64, error) {
	return s.dao.List(ctx, ListFilter{UserID: userID, Status: status}, offset, limit)
}

func (s *Service) AdminListOrders(ctx context.Context, f ListFilter, offset, limit int) ([]Order, int64, error) {
	return s.dao.List(ctx, f, offset, limit)
}

// AdminForcePaid 管理员手工将 pending 订单置为已支付并入账(发卡出错时的应急通道)。
func (s *Service) AdminForcePaid(ctx context.Context, orderID uint64, actorID uint64) error {
	tx, err := s.dao.DB().BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	o, err := loadOrderForUpdate(ctx, tx, orderID)
	if err != nil {
		return err
	}
	if o.Status != StatusPending {
		return ErrOrderStateInvalid
	}
	paidAt := nowUTC()
	_, err = tx.ExecContext(ctx,
		`UPDATE recharge_orders
           SET status = ?, paid_at = ?, trade_no = IFNULL(NULLIF(trade_no,''), ?)
         WHERE id = ?`,
		StatusPaid, paidAt, fmt.Sprintf("manual-%d", actorID), orderID)
	if err != nil {
		return err
	}
	refID := fmt.Sprintf("order:%s", o.OutTradeNo)
	remark := fmt.Sprintf("管理员手工入账:%s by admin=%d", o.Remark, actorID)
	if err := s.billing.RechargeTx(ctx, tx, o.UserID, o.TotalCredits(), refID, remark); err != nil {
		return err
	}
	return tx.Commit()
}

// ---------- helpers ----------

// genTradeNo 生成 32 位小写 hex。用 crypto/rand 防撞(绝对不会用 time-based)。
func genTradeNo() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// rawDump 把回调原文拼成 "k=v&k=v",方便排查。不参与签名。
func rawDump(m map[string]string) *string {
	if len(m) == 0 {
		return nil
	}
	v := url.Values{}
	for k, vv := range m {
		v.Set(k, vv)
	}
	s := v.Encode()
	return &s
}

func (s *Service) insertOrder(ctx context.Context, o *Order) error {
	cap := int64(0)
	expireMinutes := 30
	if s.settings != nil {
		cap = s.settings.RechargeDailyLimitCNY()
		expireMinutes = s.OrderExpireMinutes()
	}
	if cap <= 0 {
		_, err := s.dao.CreateOrder(ctx, o)
		return err
	}

	tx, err := s.dao.DB().BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if err := lockActiveUserForUpdate(ctx, tx, o.UserID); err != nil {
		return err
	}
	reserved, err := sumReservedRechargeCNYTx(ctx, tx, o.UserID, expireMinutes)
	if err != nil {
		return err
	}
	if reserved+int64(o.PriceCNY) > cap {
		return ErrDailyLimitExceeded
	}
	if err := createOrderTx(ctx, tx, o); err != nil {
		return err
	}
	return tx.Commit()
}

func notifyPayableStatus(status string) bool {
	switch status {
	case StatusPending, StatusCancelled, StatusExpired:
		return true
	default:
		return false
	}
}

func loadOrderForUpdate(ctx context.Context, tx *sqlx.Tx, id uint64) (*Order, error) {
	var o Order
	err := tx.GetContext(ctx, &o,
		`SELECT * FROM recharge_orders WHERE id = ? FOR UPDATE`, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &o, err
}

func lockActiveUserForUpdate(ctx context.Context, tx *sqlx.Tx, userID uint64) error {
	var id uint64
	err := tx.QueryRowxContext(ctx,
		`SELECT id FROM users WHERE id = ? AND deleted_at IS NULL FOR UPDATE`, userID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("user %d not found", userID)
	}
	return err
}

func sumReservedRechargeCNYTx(ctx context.Context, tx *sqlx.Tx, userID uint64, expireMinutes int) (int64, error) {
	if expireMinutes <= 0 {
		expireMinutes = 30
	}
	var sum sql.NullInt64
	q := fmt.Sprintf(`SELECT COALESCE(SUM(price_cny), 0) FROM recharge_orders
         WHERE user_id = ?
           AND (
             (status = 'paid' AND paid_at >= CURDATE())
             OR
             (status = 'pending' AND created_at >= (NOW() - INTERVAL %d MINUTE))
           )`, expireMinutes)
	if err := tx.GetContext(ctx, &sum, q, userID); err != nil {
		return 0, err
	}
	return sum.Int64, nil
}

func createOrderTx(ctx context.Context, tx *sqlx.Tx, o *Order) error {
	res, err := tx.ExecContext(ctx,
		`INSERT INTO recharge_orders
           (out_trade_no, user_id, package_id, price_cny, credits, bonus,
            channel, pay_method, status, trade_no, pay_url, client_ip, remark)
         VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		o.OutTradeNo, o.UserID, o.PackageID, o.PriceCNY, o.Credits, o.Bonus,
		o.Channel, o.PayMethod, o.Status, o.TradeNo, o.PayURL, o.ClientIP, o.Remark)
	if err != nil {
		return err
	}
	id, _ := res.LastInsertId()
	o.ID = uint64(id)
	return nil
}

// verifyAmount 把 "12.00" 和 1200(分) 对比。
func verifyAmount(money string, wantFen int) error {
	var f float64
	if _, err := fmt.Sscanf(money, "%f", &f); err != nil {
		return fmt.Errorf("invalid money: %w", err)
	}
	got := int(f*100 + 0.5)
	if got != wantFen {
		return fmt.Errorf("amount mismatch: got %d fen, want %d", got, wantFen)
	}
	return nil
}

// nowUTC 抽离以便单测 stub。
var nowUTC = defaultNowUTC
