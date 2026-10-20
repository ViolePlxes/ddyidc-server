package instance

import (
	"context"
	dom "ddyidc/internal/instance/domain"
	repo "ddyidc/internal/instance/repository"
	"ddyidc/internal/notification"
	order "ddyidc/internal/order/domain"
	orderrepo "ddyidc/internal/order/repository"
	ordersvc "ddyidc/internal/order/service"
	paydom "ddyidc/internal/payment/domain"
	payment "ddyidc/internal/payment/repository"
	"ddyidc/internal/shared/renewalquote"
	"ddyidc/internal/shared/testenv"
	"ddyidc/internal/upstream/model"
	"ddyidc/internal/upstream/provider"
	"ddyidc/internal/upstream/provider/contracts"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"strings"
	"testing"
	"time"
)

type gateRenewalFixture struct {
	provider.UpstreamProvider
	contracts.InstanceProvider
	contracts.RenewalProvider
	retail, cost string
	duration     int64
	expiry       time.Time
}

func (p *gateRenewalFixture) WithInstanceContext(context.Context) contracts.InstanceProvider {
	return p
}
func (p *gateRenewalFixture) GetInstanceDetail(id int64) (*contracts.InstanceDetail, error) {
	return &contracts.InstanceDetail{ID: fmt.Sprint(id), Status: "Active", ProductID: "1920", ExpiresAt: &p.expiry}, nil
}
func (p *gateRenewalFixture) RenewalQuotes(int64) ([]contracts.RenewalQuote, error) {
	return []contracts.RenewalQuote{{Cycle: "monthly", OriginalAmount: p.retail, Amount: p.cost, Duration: p.duration}}, nil
}
func seedGateSupplier(t *testing.T, db *gorm.DB, id, product int64) {
	t.Helper()
	s := model.Supplier{ID: fmt.Sprint(id), Name: "renewal-gate-isolated", Type: "finance", Status: 1, URL: "https://fixture.invalid", Username: "fixture", Token: "cipher", Secret: "cipher", Rate: 1}
	if e := db.Create(&s).Error; e != nil {
		t.Fatal(e)
	}
	m := model.UpstreamProduct{SupplierID: s.ID, UpstreamProductID: 1920, ProductID: fmt.Sprint(product), ProductGroupID: "0", Name: "fixture", Mode: "only_api", PriceBasis: "standard"}
	if e := db.Create(&m).Error; e != nil {
		t.Fatal(e)
	}
}
func TestRenewalQuoteGateMySQL(t *testing.T) {
	db := testenv.MySQL(t)
	tx := db.Begin()
	defer tx.Rollback()
	ctx := context.Background()
	sid := int64(986001)
	seedGateSupplier(t, tx, sid, 986002)
	h := dom.Host{ID: 986003, InstanceNo: "RENEWAL-GATE", CustomerID: 986004, ProductID: 986002, ModuleType: "cloud", ConfigSnapshot: `{"billingCycle":"monthly"}`, Status: 3, UpstreamProviderID: &sid, UpstreamInstanceID: "84042", OwnershipVersion: 1}
	if e := tx.Create(&h).Error; e != nil {
		t.Fatal(e)
	}
	o := order.Order{ID: 986005, OrderNo: "REN-GATE-OLD", CustomerID: h.CustomerID, TotalAmount: 3177, PayAmount: 3177, Status: 1}
	if e := tx.Create(&o).Error; e != nil {
		t.Fatal(e)
	}
	item := order.OrderItem{OrderID: o.ID, ProductID: h.ProductID, ProductName: "fixture", ModuleType: "instance_renewal", Qty: 1, UnitPrice: 3177, Subtotal: 3177, InstanceID: &h.ID, ConfigSnapshot: "{}"}
	if e := tx.Create(&item).Error; e != nil {
		t.Fatal(e)
	}
	r := Renewal{ID: 986006, OrderID: o.ID, InstanceID: h.ID, CustomerID: h.CustomerID, OwnershipVersion: 1, SupplierID: sid, HostID: h.UpstreamInstanceID, Cycle: "monthly", Duration: 2678400, Amount: 3177, Cost: 2700, State: "unpaid", BaseExpiry: time.Now().Add(time.Hour)}
	if e := tx.Create(&r).Error; e != nil {
		t.Fatal(e)
	}
	f := &gateRenewalFixture{retail: "30.00", cost: "27.00", duration: r.Duration, expiry: r.BaseExpiry}
	svc := NewService(repo.NewRepository(tx), securityResolver{provider: f})
	svc.db = tx
	_, e := svc.VerifyRenewalPayment(ctx, h.CustomerID, o.ID)
	var changed *renewalquote.Error
	if !errors.As(e, &changed) || changed.Code != "RENEWAL_PRICE_CHANGED" || changed.NewAmount != "30.00" {
		t.Fatalf("old incorrect price accepted: %v", e)
	}
	var count int64
	tx.Model(&renewalquote.Evidence{}).Where("order_id=?", o.ID).Count(&count)
	if count != 0 {
		t.Fatal("incorrect order got proof")
	}
	if e := renewalquote.CheckTx(ctx, tx, h.CustomerID, o.ID); e == nil {
		t.Fatal("repository accepted absent proof")
	}

	if e := tx.Model(&o).Update("status", 2).Error; e != nil {
		t.Fatal(e)
	}
	handled, deliveryErr := svc.ProvisionRenewal(ctx, h.CustomerID, o.ID, "alipay")
	if !handled || deliveryErr == nil {
		t.Fatal("historical paid wrong quote auto-fulfilled")
	}
	if e := tx.Model(&o).Update("status", 1).Error; e != nil {
		t.Fatal(e)
	}
	// Fixture-only correction creates the positive scenario; production orders are never changed this way.
	if e := tx.Model(&o).Updates(map[string]any{"total_amount": 3000, "pay_amount": 2900, "discount_amount": 100, "coupon_amount": 100}).Error; e != nil {
		t.Fatal(e)
	}
	if e := tx.Model(&r).Update("amount", "29.00").Error; e != nil {
		t.Fatal(e)
	}
	if e := tx.Model(&item).Updates(map[string]any{"unit_price": 3000, "subtotal": 3000}).Error; e != nil {
		t.Fatal(e)
	}

	proofCtx, e := svc.VerifyRenewalPayment(ctx, h.CustomerID, o.ID)
	if e != nil {
		t.Fatal("valid coupon rejected", e)
	}
	if e := renewalquote.CheckTx(proofCtx, tx, h.CustomerID, o.ID); e != nil {
		t.Fatal(e)
	}

	scoped, scopeErr := svc.AuditRenewalQuotes(ctx, AdminAccess{CustomerIDs: []int64{h.CustomerID + 1}}, 1)
	if scopeErr != nil || scoped.Total != 0 {
		t.Fatal("renewal audit escaped customer scope", scopeErr)
	}
	scoped, scopeErr = svc.AuditRenewalQuotes(ctx, AdminAccess{CustomerIDs: []int64{h.CustomerID}}, 1)
	if scopeErr != nil || scoped.Total != 1 || !scoped.ReadOnly || scoped.LiveQuote {
		t.Fatal("scoped local audit invalid", scopeErr)
	}
	pr := payment.NewRepository(tx)
	if _, e := pr.PrepareOrder(ctx, h.CustomerID, o.ID, "wechat", time.Now().Add(time.Minute)); e == nil {
		t.Fatal("online entry bypassed proof")
	}
	prepared, e := pr.PrepareOrder(proofCtx, h.CustomerID, o.ID, "wechat", time.Now().Add(time.Minute))
	if e != nil {
		t.Fatal(e)
	}
	if prepared.Payment.AmountFen != 2900 {
		t.Fatal("coupon net changed")
	}
	if e := renewalquote.ReceiptAllowedTx(ctx, tx, h.CustomerID, o.ID, prepared.Payment.ID, 2900, time.Now()); e != nil {
		t.Fatal(e)
	}
	// Ownership changes after preflight are rejected inside the payment transaction.
	if e := tx.Model(&h).Update("ownership_version", 2).Error; e != nil {
		t.Fatal(e)
	}
	if e := renewalquote.CheckTx(proofCtx, tx, h.CustomerID, o.ID); e == nil {
		t.Fatal("stale owner accepted")
	}
	if e := tx.Model(&h).Update("ownership_version", 1).Error; e != nil {
		t.Fatal(e)
	}

	// Quote expiry and FX changes fail inside the final transaction, not just the UI.
	if e := tx.Table("common.suppliers").Where("id=?", sid).Update("rate", 2).Error; e != nil {
		t.Fatal(e)
	}
	if e := renewalquote.CheckTx(proofCtx, tx, h.CustomerID, o.ID); e == nil {
		t.Fatal("changed currency rate accepted")
	}
	if e := tx.Table("common.suppliers").Where("id=?", sid).Update("rate", 1).Error; e != nil {
		t.Fatal(e)
	}
	if e := renewalquote.ReceiptAllowedTx(ctx, tx, h.CustomerID, o.ID, prepared.Payment.ID, 2900, time.Now().Add(10*time.Minute)); e == nil {
		t.Fatal("expired quote accepted captured payment for automatic delivery")
	}
	// A verified old payment without matching evidence is retained for manual review.
	old := paydom.Payment{ID: 986009, PaymentNo: "RENEW-GATE-OLD-PAY", BizType: paydom.BizOrder, BizID: o.ID, CustomerID: h.CustomerID, Channel: "alipay", OutTradeNo: "RENEW-GATE-OLD-PAY", Amount: "29.00", AmountFen: 2900, Currency: "CNY", Status: paydom.StatusPending, ExpireAt: time.Now().Add(time.Minute)}
	if e := tx.Create(&old).Error; e != nil {
		t.Fatal(e)
	}
	captured, transitioned, e := pr.Complete(ctx, paydom.VerifiedNotification{Channel: "alipay", OutTradeNo: old.OutTradeNo, TransactionID: "RENEW-GATE-CAPTURE", AmountFen: 2900, Currency: "CNY", PaidAt: time.Now()})
	if e != nil || transitioned || captured.Status != paydom.StatusReview {
		t.Fatalf("late receipt lost or delivered: %+v %v %v", captured, transitioned, e)
	}
	tx.Table("payment.receipts").Where("payment_id=?", old.ID).Count(&count)
	if count != 1 {
		t.Fatal("captured receipt missing")
	}
	if _, again, e := pr.Complete(ctx, paydom.VerifiedNotification{Channel: "alipay", OutTradeNo: old.OutTradeNo, TransactionID: "RENEW-GATE-CAPTURE", AmountFen: 2900, Currency: "CNY", PaidAt: time.Now()}); e != nil || again {
		t.Fatal("duplicate reviewed receipt replayed", e)
	}
	tx.Table("payment.receipts").Where("payment_id=?", old.ID).Count(&count)
	if count != 1 {
		t.Fatal("review receipt duplicated")
	}

	t.Log("old3177 blocked; coupon gross30/net29 accepted; online bypass rejected; ownership race rejected; unverified captured receipt retained for review")
}

type renewalReplacementHooks struct{ releases int }

func (*renewalReplacementHooks) ChargeCoupon(context.Context, *gorm.DB, int64, int64, int64, int64) (int64, error) {
	return 0, nil
}
func (*renewalReplacementHooks) OnOrderPaid(context.Context, int64, string, int64, int64) error {
	return nil
}
func (*renewalReplacementHooks) OnRechargePaid(context.Context, int64, string, int64, int64) error {
	return nil
}
func (h *renewalReplacementHooks) ReleaseCoupon(context.Context, *gorm.DB, int64) error {
	h.releases++
	return nil
}
func TestRenewalQuoteReplacementMySQL(t *testing.T) {
	db := testenv.MySQL(t)
	tx := db.Begin()
	defer tx.Rollback()
	ctx := context.Background()
	sid := int64(987001)
	seedGateSupplier(t, tx, sid, 987002)
	expiry := time.Now().Add(time.Hour).Truncate(time.Second)
	h := dom.Host{ID: 987003, InstanceNo: "RENEWAL-REPLACE", CustomerID: 987004, ProductID: 987002, ModuleType: "cloud", ConfigSnapshot: `{"billingCycle":"monthly"}`, Status: 3, UpstreamProviderID: &sid, UpstreamInstanceID: "84042", OwnershipVersion: 1, ExpiresAt: &expiry}
	if e := tx.Create(&h).Error; e != nil {
		t.Fatal(e)
	}
	o := order.Order{ID: 987005, OrderNo: "REN-REPLACE-OLD", CustomerID: h.CustomerID, TotalAmount: 3177, PayAmount: 3177, Status: 1}
	if e := tx.Create(&o).Error; e != nil {
		t.Fatal(e)
	}
	rr := Renewal{ID: 987006, OrderID: o.ID, InstanceID: h.ID, CustomerID: h.CustomerID, OwnershipVersion: 1, SupplierID: sid, HostID: h.UpstreamInstanceID, Cycle: "monthly", Duration: 2678400, Amount: 3177, Cost: 2700, State: "unpaid", BaseExpiry: expiry}
	if e := tx.Create(&rr).Error; e != nil {
		t.Fatal(e)
	}
	svc := NewService(repo.NewRepository(tx), securityResolver{provider: &gateRenewalFixture{retail: "30.00", cost: "27.00", duration: rr.Duration, expiry: expiry}})
	svc.db = tx
	hooks := &renewalReplacementHooks{}
	orders := ordersvc.NewService(orderrepo.NewRepository(tx), hooks)
	svc.SetRenewalReplacementCanceller(orders.CancelRenewalReplacementTx)
	options, e := svc.RenewalOptions(ctx, h.CustomerID, h.ID)
	if e != nil {
		t.Fatal(e)
	}
	intent := RenewalIntent{QuoteRevision: options[0].QuoteRevision, ReplaceOrderID: fmt.Sprint(o.ID)}
	if _, e := svc.CreateRenewalIntent(ctx, h.CustomerID, h.ID, "monthly", RenewalIntent{QuoteRevision: "stale", ReplaceOrderID: intent.ReplaceOrderID}); e == nil {
		t.Fatal("stale price consent accepted")
	}
	pending := paydom.Payment{ID: 987007, PaymentNo: "RENEW-REPLACE-PAY", BizType: paydom.BizOrder, BizID: o.ID, CustomerID: h.CustomerID, Channel: "wechat", OutTradeNo: "RENEW-REPLACE-PAY", Amount: "31.77", AmountFen: 3177, Currency: "CNY", Status: paydom.StatusClosed, ExpireAt: expiry}
	if e := tx.Create(&pending).Error; e != nil {
		t.Fatal(e)
	}
	if _, e := svc.CreateRenewalIntent(ctx, h.CustomerID, h.ID, "monthly", intent); e == nil {
		t.Fatal("local closed flag allowed unsafe replacement")
	}
	var current order.Order
	tx.Where("id=?", o.ID).Take(&current)
	if current.Status != 1 {
		t.Fatal("blocked replacement cancelled original")
	}
	if e := tx.Delete(&pending).Error; e != nil {
		t.Fatal(e)
	} // only isolated fixture, no real payment is deleted
	replaced, e := svc.CreateRenewalIntent(ctx, h.CustomerID, h.ID, "monthly", RenewalIntent{QuoteRevision: intent.QuoteRevision})
	if e != nil {
		t.Fatal(e)
	}
	repeated, e := svc.CreateRenewalIntent(ctx, h.CustomerID, h.ID, "monthly", intent)
	if e != nil || repeated.OrderID != replaced.OrderID {
		t.Fatal("replacement replay made second order", e)
	}
	tx.Where("id=?", o.ID).Take(&current)
	if current.Status != 3 || current.TotalAmount != 3177 || hooks.releases != 1 || replaced.Amount != "30.00" {
		t.Fatalf("replacement audit changed original amount: %+v %+v releases=%d", current, replaced, hooks.releases)
	}
	var count int64
	tx.Model(&Renewal{}).Where("instance_id=? AND state='unpaid'", h.ID).Count(&count)
	if count != 1 {
		t.Fatal("multiple payable renewal intentions")
	}
	t.Log("safe repricing requires no extra confirmation; any past channel payment blocks; replacement atomic, retains old audit and replays idempotently")
}

func TestAutoRenewalQuotePauseMySQL(t *testing.T) {
	db := testenv.MySQL(t)
	tx := db.Begin()
	defer tx.Rollback()
	ctx := context.Background()
	sid := int64(988001)
	seedGateSupplier(t, tx, sid, 988002)
	expiry := time.Now().Add(time.Hour)
	h := dom.Host{ID: 988003, InstanceNo: "AUTO-QUOTE", CustomerID: 988004, ProductID: 988002, ModuleType: "cloud", ConfigSnapshot: `{"billingCycle":"monthly"}`, Status: 3, UpstreamProviderID: &sid, UpstreamInstanceID: "84042", OwnershipVersion: 1, ExpiresAt: &expiry}
	if e := tx.Create(&h).Error; e != nil {
		t.Fatal(e)
	}
	svc := NewService(repo.NewRepository(tx), securityResolver{provider: &gateRenewalFixture{retail: "30.00", cost: "27.00", duration: 2678400, expiry: expiry}})
	svc.db = tx
	if e := tx.Create(&RenewalSettings{InstanceID: h.ID, CustomerID: h.CustomerID, Enabled: true, UpdatedAt: time.Now()}).Error; e != nil {
		t.Fatal(e)
	}
	if svc.autoRenewalQuoteAllowed(ctx, h, "monthly") {
		t.Fatal("old auto setting missing explicit approval allowed")
	}
	var setting RenewalSettings
	tx.Where("instance_id=?", h.ID).Take(&setting)
	if setting.Enabled {
		t.Fatal("unverified auto renewal not paused")
	}

	if e := svc.SetAutoRenew(ctx, h.CustomerID, h.ID, true); e == nil {
		t.Fatal("enabled auto without explicit price consent")
	}
	if e := svc.SetAutoRenew(ctx, h.CustomerID, h.ID, true, "stale"); e == nil {
		t.Fatal("enabled auto with stale consent")
	}
	options, quoteErr := svc.RenewalOptions(ctx, h.CustomerID, h.ID)
	if quoteErr != nil {
		t.Fatal(quoteErr)
	}
	if e := svc.SetAutoRenew(ctx, h.CustomerID, h.ID, true, options[0].QuoteRevision); e != nil {
		t.Fatal(e)
	}
	if !svc.autoRenewalQuoteAllowed(ctx, h, "monthly") {
		t.Fatal("fresh exact price not authorized")
	}

	fixture := svc.providers.(securityResolver).provider.(*gateRenewalFixture)
	fixture.cost = "26.00"
	if !svc.autoRenewalQuoteAllowed(ctx, h, "monthly") {
		t.Fatal("procurement discount changed an unchanged retail authorization")
	}
	fixture.cost = "27.00"
	// A stale task must not disable a user's newer approval.
	svc.pauseAutoRenewalQuote(ctx, h, "price_changed", "31.77", "30.00", "outdated")
	tx.Where("instance_id=?", h.ID).Take(&setting)
	if !setting.Enabled {
		t.Fatal("stale worker disabled newly approved quote")
	}
	if e := tx.Model(&AutoRenewalQuote{}).Where("instance_id=?", h.ID).Updates(map[string]any{"amount": "31.77", "quote_revision": "old-price"}).Error; e != nil {
		t.Fatal(e)
	}
	if svc.autoRenewalQuoteAllowed(ctx, h, "monthly") {
		t.Fatal("changed price auto-debited")
	}
	svc.pauseAutoRenewalQuote(ctx, h, "price_changed", "31.77", "30.00", "old-price")
	var count int64
	tx.Model(&RenewalQuoteAlert{}).Where("instance_id=? AND reason='price_changed'", h.ID).Count(&count)
	if count != 1 {
		t.Fatal("auto price alert was duplicated")
	}
	var queued int64
	if err := tx.Model(&notification.Outbox{}).Where("event_key LIKE ?", fmt.Sprintf("%d:renewal_quote:%d:%%", h.CustomerID, h.ID)).Count(&queued).Error; err != nil || queued != 2 {
		t.Fatalf("expected two durable distinct alerts, got %d: %v", queued, err)
	}
	if svc.AutoRenewalWarning(ctx, h.CustomerID, h.ID) == "" {
		t.Fatal("paused reason invisible")
	}
	t.Log("legacy auto approval paused; explicit fresh approval accepted; changed price pauses without debit; stale worker rejected; alert deduplicated")
}

func TestRenewalQuoteRuntimePrivilegesMySQL(t *testing.T) {
	db := testenv.MySQL(t)
	pool, e := db.DB()
	if e != nil {
		t.Fatal(e)
	}
	ctx := context.Background()
	// Only an explicitly isolated --skip-networking fixture is accepted by testenv.
	if _, e := pool.Exec("CREATE USER IF NOT EXISTS 'ddyidc_runtime'@'127.0.0.1'"); e != nil {
		t.Fatal(e)
	}
	if e := ApplyRuntimePrivileges(ctx, pool, "wrong_identity", "127.0.0.1"); e == nil {
		t.Fatal("unexpected runtime identity accepted")
	}
	if e := ApplyRuntimePrivileges(ctx, pool, "ddyidc_runtime", "127.0.0.1"); e != nil {
		t.Fatal(e)
	}
	for _, table := range []string{"instance_renewal_quote_evidence", "instance_renewal_payment_quotes", "instance_renewal_quote_alerts"} {
		var count int64
		if e := db.Raw("SELECT COUNT(*) FROM information_schema.TABLE_PRIVILEGES WHERE GRANTEE=? AND TABLE_SCHEMA='instance' AND TABLE_NAME=? AND PRIVILEGE_TYPE IN ('SELECT','INSERT')", "'ddyidc_runtime'@'127.0.0.1'", table).Scan(&count).Error; e != nil || count != 2 {
			t.Fatal("required read/append grant missing", table, e, count)
		}
		if e := db.Raw("SELECT COUNT(*) FROM information_schema.TABLE_PRIVILEGES WHERE GRANTEE=? AND TABLE_SCHEMA='instance' AND TABLE_NAME=? AND PRIVILEGE_TYPE IN ('UPDATE','DELETE','ALTER','DROP')", "'ddyidc_runtime'@'127.0.0.1'", table).Scan(&count).Error; e != nil || count != 0 {
			t.Fatal("immutable proof grant too broad", table, e, count)
		}
	}
	var columns int64
	if e := db.Raw("SELECT COUNT(*) FROM information_schema.COLUMN_PRIVILEGES WHERE GRANTEE=? AND TABLE_SCHEMA='instance' AND TABLE_NAME='instance_renewal_auto_quotes' AND PRIVILEGE_TYPE='UPDATE'", "'ddyidc_runtime'@'127.0.0.1'").Scan(&columns).Error; e != nil || columns != 6 {
		t.Fatal("auto approval column grant mismatch", e, columns)
	}
	t.Log("runtime can read/append evidence; cannot update/delete proof; auto approval update limited to six columns; wrong identity refused")
}

type correctionCouponHooks struct {
	renewalReplacementHooks
	charged int
	gross   int64
}

func (h *correctionCouponHooks) ChargeCoupon(_ context.Context, _ *gorm.DB, _ int64, _ int64, _ int64, gross int64) (int64, error) {
	h.charged++
	h.gross = gross
	return 100, nil
}
func TestRenewalQuoteCorrectionMySQL(t *testing.T) {
	db := testenv.MySQL(t)
	tx := db.Begin()
	defer tx.Rollback()
	ctx := context.Background()
	sid := int64(989001)
	seedGateSupplier(t, tx, sid, 989002)
	expiry := time.Now().Add(time.Hour).Truncate(time.Second)
	host := dom.Host{ID: 989003, InstanceNo: "SILENT-PRICE", CustomerID: 989004, ProductID: 989002, ModuleType: "cloud", ConfigSnapshot: `{"billingCycle":"monthly"}`, Status: 3, UpstreamProviderID: &sid, UpstreamInstanceID: "84042", OwnershipVersion: 1, ExpiresAt: &expiry}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(tx.Create(&host).Error)
	o := order.Order{ID: 989005, OrderNo: "SILENT-RENEW", CustomerID: host.CustomerID, TotalAmount: 3177, PayAmount: 3177, Status: 1}
	must(tx.Create(&o).Error)
	item := order.OrderItem{OrderID: o.ID, ProductID: host.ProductID, ProductName: "fixture", ModuleType: "instance_renewal", Qty: 1, UnitPrice: 3177, Subtotal: 3177, InstanceID: &host.ID, ConfigSnapshot: "{}"}
	must(tx.Create(&item).Error)
	row := Renewal{ID: 989006, OrderID: o.ID, InstanceID: host.ID, CustomerID: host.CustomerID, OwnershipVersion: 1, SupplierID: sid, HostID: host.UpstreamInstanceID, Cycle: "monthly", Duration: 2678400, Amount: 3177, Cost: 2700, State: "unpaid", BaseExpiry: expiry}
	must(tx.Create(&row).Error)
	fixture := &gateRenewalFixture{retail: "30.00", cost: "27.00", duration: row.Duration, expiry: expiry}
	svc := NewService(repo.NewRepository(tx), securityResolver{provider: fixture})
	svc.db = tx
	hooks := &correctionCouponHooks{}
	orders := ordersvc.NewService(orderrepo.NewRepository(tx), hooks)
	svc.SetRenewalPriceCorrector(orders.CorrectUnpaidRenewalPriceTx)
	proof, err := svc.VerifyRenewalPayment(ctx, host.CustomerID, o.ID)
	must(err)
	must(renewalquote.CheckTx(proof, tx, host.CustomerID, o.ID))
	var got order.Order
	must(tx.Where("id=?", o.ID).Take(&got).Error)
	if got.TotalAmount != 3000 || got.PayAmount != 3000 || got.Status != 1 || got.ID != o.ID {
		t.Fatalf("unpaid original not corrected in place: %+v", got)
	}
	var audits int64
	must(tx.Table("system.audit_logs").Where("action='renewal_price_corrected' AND target_id=?", o.ID).Count(&audits).Error)
	if audits != 1 {
		t.Fatal("correction audit missing", audits)
	}
	var audit struct{ Detail string }
	must(tx.Table("system.audit_logs").Select("detail").Where("action='renewal_price_corrected' AND target_id=?", o.ID).Take(&audit).Error)
	if !strings.Contains(audit.Detail, "31.77") || !strings.Contains(audit.Detail, "30.00") {
		t.Fatal("original/new amounts missing from audit")
	}
	_, err = svc.VerifyRenewalPayment(ctx, host.CustomerID, o.ID)
	must(err)
	must(tx.Table("system.audit_logs").Where("action='renewal_price_corrected' AND target_id=?", o.ID).Count(&audits).Error)
	if audits != 1 {
		t.Fatal("repeat corrected twice")
	}
	prepared, err := payment.NewRepository(tx).PrepareOrder(proof, host.CustomerID, o.ID, "wechat", time.Now().Add(time.Minute))
	must(err)
	if prepared.Payment.AmountFen != 3000 {
		t.Fatal("gateway still uses old3177", prepared.Payment.AmountFen)
	}
	fixture.retail = "35.00"
	if _, err = svc.VerifyRenewalPayment(ctx, host.CustomerID, o.ID); err == nil {
		t.Fatal("existing channel checkout repriced")
	}
	must(tx.Where("id=?", o.ID).Take(&got).Error)
	if got.TotalAmount != 3000 {
		t.Fatal("inflight order mutated")
	}
	// Only this isolated fixture is reset to exercise the coupon recalculation contract.
	must(tx.Delete(prepared.Payment).Error)
	fixture.retail = "30.00"
	coupon := int64(123)
	must(tx.Model(&got).Updates(map[string]any{"total_amount": 3177, "pay_amount": 3077, "discount_amount": 100, "coupon_amount": 100, "coupon_id": coupon}).Error)
	must(tx.Model(&row).Update("amount", "30.77").Error)
	must(tx.Model(&item).Updates(map[string]any{"unit_price": 3177, "subtotal": 3177}).Error)
	options, e := svc.RenewalOptions(ctx, host.CustomerID, host.ID)
	must(e)
	corrected, e := svc.CreateRenewalIntent(ctx, host.CustomerID, host.ID, "monthly", RenewalIntent{QuoteRevision: options[0].QuoteRevision})
	must(e)
	if corrected.OrderID != fmt.Sprint(o.ID) || corrected.Amount != "29.00" {
		t.Fatal("create did not silently preserve corrected order/coupon", corrected)
	}
	proof, err = svc.VerifyRenewalPayment(ctx, host.CustomerID, o.ID)
	must(err)
	must(renewalquote.CheckTx(proof, tx, host.CustomerID, o.ID))
	must(tx.Where("id=?", o.ID).Take(&got).Error)
	if got.TotalAmount != 3000 || got.PayAmount != 2900 || hooks.charged != 0 || hooks.releases != 0 {
		t.Fatalf("coupon correction disagrees: %+v hooks=%+v", got, hooks)
	}
	t.Log("silent3177->3000 correction keeps orderID; one audit retains old amount; replay idempotent; gateway receives3000; pending channel blocks mutation; reserved coupon remains100 without redemption/ledger changes; proof matches2900 net")
}
