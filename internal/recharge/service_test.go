package recharge

import (
	"context"
	"errors"
	"net/url"
	"reflect"
	"regexp"
	"testing"
	"time"
	"unsafe"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	"go.uber.org/zap"

	"github.com/432539/gpt2api/internal/billing"
	"github.com/432539/gpt2api/internal/config"
	"github.com/432539/gpt2api/internal/settings"
)

func TestHandleNotifySettlesExpiredOrderAtomically(t *testing.T) {
	svc, mock, cleanup := newRechargeServiceForTest(t)
	defer cleanup()

	order := testOrder(StatusExpired)
	expectOrderByOutTradeNo(mock, order)

	paidAt := time.Date(2026, 4, 23, 10, 11, 12, 0, time.UTC)
	restoreNow := stubNowUTC(paidAt)
	defer restoreNow()

	mock.ExpectBegin()
	expectLockedOrder(mock, order)
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE recharge_orders
           SET status = ?, trade_no = ?, pay_method = ?, paid_at = ?, notify_raw = ?
         WHERE id = ?`)).
		WithArgs(StatusPaid, "epay-trade-1", "alipay", paidAt, sqlmock.AnyArg(), order.ID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	expectRechargeCredit(mock, order, 2300, "order:"+order.OutTradeNo, "充值:"+order.Remark, false)
	mock.ExpectCommit()

	text, err := svc.HandleNotify(context.Background(), signedNotifyForm(svc, order.OutTradeNo, "epay-trade-1", "12.00"))
	if err != nil {
		t.Fatalf("HandleNotify returned error: %v", err)
	}
	if text != "success" {
		t.Fatalf("unexpected notify response: %q", text)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestHandleNotifyRollsBackWhenRechargeFails(t *testing.T) {
	svc, mock, cleanup := newRechargeServiceForTest(t)
	defer cleanup()

	order := testOrder(StatusPending)
	expectOrderByOutTradeNo(mock, order)

	paidAt := time.Date(2026, 4, 23, 12, 0, 0, 0, time.UTC)
	restoreNow := stubNowUTC(paidAt)
	defer restoreNow()

	mock.ExpectBegin()
	expectLockedOrder(mock, order)
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE recharge_orders
           SET status = ?, trade_no = ?, pay_method = ?, paid_at = ?, notify_raw = ?
         WHERE id = ?`)).
		WithArgs(StatusPaid, "epay-trade-2", "alipay", paidAt, sqlmock.AnyArg(), order.ID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	expectRechargeCredit(mock, order, 2300, "order:"+order.OutTradeNo, "充值:"+order.Remark, true)
	mock.ExpectRollback()

	text, err := svc.HandleNotify(context.Background(), signedNotifyForm(svc, order.OutTradeNo, "epay-trade-2", "12.00"))
	if err == nil {
		t.Fatal("expected HandleNotify to fail when recharge credit insert fails")
	}
	if text != "fail" {
		t.Fatalf("unexpected notify response: %q", text)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestAdminForcePaidRollsBackWhenRechargeFails(t *testing.T) {
	svc, mock, cleanup := newRechargeServiceForTest(t)
	defer cleanup()

	order := testOrder(StatusPending)

	paidAt := time.Date(2026, 4, 23, 13, 0, 0, 0, time.UTC)
	restoreNow := stubNowUTC(paidAt)
	defer restoreNow()

	mock.ExpectBegin()
	expectLockedOrder(mock, order)
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE recharge_orders
           SET status = ?, paid_at = ?, trade_no = IFNULL(NULLIF(trade_no,''), ?)
         WHERE id = ?`)).
		WithArgs(StatusPaid, paidAt, "manual-99", order.ID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	expectRechargeCredit(
		mock,
		order,
		2300,
		"order:"+order.OutTradeNo,
		"管理员手工入账:"+order.Remark+" by admin=99",
		true,
	)
	mock.ExpectRollback()

	err := svc.AdminForcePaid(context.Background(), order.ID, 99)
	if err == nil {
		t.Fatal("expected AdminForcePaid to fail when recharge credit insert fails")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestCreateRejectsWhenActiveReservationsReachDailyLimit(t *testing.T) {
	svc, mock, cleanup := newRechargeServiceForTest(t)
	defer cleanup()

	svc.SetSettings(newRechargeSettingsForTest(map[string]string{
		settings.RechargeEnabled:            "true",
		settings.RechargeDailyLimitCNY:      "1500",
		settings.RechargeOrderExpireMinutes: "30",
	}))

	mock.ExpectQuery(regexp.QuoteMeta(`SELECT * FROM recharge_packages WHERE id = ?`)).
		WithArgs(uint64(3)).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "name", "price_cny", "credits", "bonus", "description", "sort", "enabled", "created_at", "updated_at",
		}).AddRow(3, "标准包", 1200, 2000, 300, "", 0, true, time.Now(), time.Now()))
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT id FROM users WHERE id = ? AND deleted_at IS NULL FOR UPDATE`)).
		WithArgs(uint64(42)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(42))
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT COALESCE(SUM(price_cny), 0) FROM recharge_orders
         WHERE user_id = ?
           AND (
             (status = 'paid' AND paid_at >= CURDATE())
             OR
             (status = 'pending' AND created_at >= (NOW() - INTERVAL 30 MINUTE))
           )`)).
		WithArgs(uint64(42)).
		WillReturnRows(sqlmock.NewRows([]string{"sum"}).AddRow(500))
	mock.ExpectRollback()

	_, err := svc.Create(context.Background(), CreateInput{
		UserID:    42,
		PackageID: 3,
		PayType:   "alipay",
		ClientIP:  "127.0.0.1",
	})
	if !errors.Is(err, ErrDailyLimitExceeded) {
		t.Fatalf("expected ErrDailyLimitExceeded, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func newRechargeServiceForTest(t *testing.T) (*Service, sqlmock.Sqlmock, func()) {
	t.Helper()

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	sqlxdb := sqlx.NewDb(db, "sqlmock")
	svc := NewService(
		NewDAO(sqlxdb),
		billing.New(sqlxdb),
		nil,
		config.EPayConfig{
			GatewayURL: "https://pay.example.com/submit.php",
			PID:        "pid-1",
			Key:        "secret-key",
			SignType:   "MD5",
			NotifyURL:  "https://app.example.com/api/public/epay/notify",
			ReturnURL:  "https://app.example.com/billing",
		},
		nil,
		"",
		zap.NewNop(),
	)
	return svc, mock, func() { _ = db.Close() }
}

func testOrder(status string) *Order {
	now := time.Date(2026, 4, 23, 8, 0, 0, 0, time.UTC)
	return &Order{
		ID:         7,
		OutTradeNo: "order-1234567890abcdef1234567890ab",
		UserID:     42,
		PackageID:  3,
		PriceCNY:   1200,
		Credits:    2000,
		Bonus:      300,
		Channel:    ChannelEPay,
		PayMethod:  "",
		Status:     status,
		TradeNo:    "",
		PayURL:     "https://pay.example.com",
		ClientIP:   "127.0.0.1",
		Remark:     "标准包",
		CreatedAt:  now,
		UpdatedAt:  now,
	}
}

func signedNotifyForm(svc *Service, outTradeNo, tradeNo, money string) url.Values {
	form := url.Values{}
	form.Set("pid", svc.cfg.PID)
	form.Set("out_trade_no", outTradeNo)
	form.Set("trade_no", tradeNo)
	form.Set("trade_status", "TRADE_SUCCESS")
	form.Set("name", "标准包")
	form.Set("money", money)
	form.Set("type", "alipay")
	form.Set("sign_type", svc.signer.SignType)

	params := map[string]string{}
	for k, vals := range form {
		params[k] = vals[0]
	}
	form.Set("sign", svc.signer.Sign(params))
	return form
}

func expectOrderByOutTradeNo(mock sqlmock.Sqlmock, order *Order) {
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT * FROM recharge_orders WHERE out_trade_no = ?`)).
		WithArgs(order.OutTradeNo).
		WillReturnRows(orderRow(order))
}

func expectLockedOrder(mock sqlmock.Sqlmock, order *Order) {
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT * FROM recharge_orders WHERE id = ? FOR UPDATE`)).
		WithArgs(order.ID).
		WillReturnRows(orderRow(order))
}

func expectRechargeCredit(mock sqlmock.Sqlmock, order *Order, balanceAfter int64, refID, remark string, failInsert bool) {
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE users SET credit_balance = credit_balance + ?, version = version + 1
         WHERE id = ? AND deleted_at IS NULL`)).
		WithArgs(order.TotalCredits(), order.UserID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT credit_balance FROM users WHERE id = ? AND deleted_at IS NULL`)).
		WithArgs(order.UserID).
		WillReturnRows(sqlmock.NewRows([]string{"credit_balance"}).AddRow(balanceAfter))

	insert := mock.ExpectExec(regexp.QuoteMeta(`INSERT INTO credit_transactions
         (user_id, key_id, type, amount, balance_after, ref_id, biz_key, remark)
         VALUES (?, ?, ?, ?, ?, ?, ?, ?)`)).
		WithArgs(order.UserID, 0, billing.KindRecharge, order.TotalCredits(), balanceAfter, refID, "recharge:"+refID, remark)
	if failInsert {
		insert.WillReturnError(errors.New("insert credit tx failed"))
		return
	}
	insert.WillReturnResult(sqlmock.NewResult(1, 1))
}

func orderRow(order *Order) *sqlmock.Rows {
	cols := []string{
		"id", "out_trade_no", "user_id", "package_id", "price_cny", "credits", "bonus",
		"channel", "pay_method", "status", "trade_no", "paid_at", "pay_url",
		"client_ip", "notify_raw", "remark", "created_at", "updated_at",
	}
	return sqlmock.NewRows(cols).AddRow(
		order.ID,
		order.OutTradeNo,
		order.UserID,
		order.PackageID,
		order.PriceCNY,
		order.Credits,
		order.Bonus,
		order.Channel,
		order.PayMethod,
		order.Status,
		order.TradeNo,
		nil,
		order.PayURL,
		order.ClientIP,
		nil,
		order.Remark,
		order.CreatedAt,
		order.UpdatedAt,
	)
}

func stubNowUTC(ts time.Time) func() {
	old := nowUTC
	nowUTC = func() time.Time { return ts }
	return func() { nowUTC = old }
}

func newRechargeSettingsForTest(values map[string]string) *settings.Service {
	ss := settings.NewService(nil)
	rv := reflect.ValueOf(ss).Elem().FieldByName("cache")
	cache := reflect.NewAt(rv.Type(), unsafe.Pointer(rv.UnsafeAddr())).Elem()
	cache.Set(reflect.ValueOf(values))
	return ss
}
