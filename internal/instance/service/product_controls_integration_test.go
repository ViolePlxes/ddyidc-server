package instance

import (
	"context"
	balance "ddyidc/internal/billing/balance"
	dom "ddyidc/internal/instance/domain"
	repo "ddyidc/internal/instance/repository"
	"ddyidc/internal/notification"
	order "ddyidc/internal/order/domain"
	"ddyidc/internal/pushtransfer"
	"ddyidc/internal/shared/productionlock"
	"ddyidc/internal/shared/testenv"
	"ddyidc/internal/upstream/provider/contracts"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

type productionLockFixture struct {
	gateRenewalFixture
	writes atomic.Int32
}

func (p *productionLockFixture) ExecuteInstanceAction(int64, contracts.InstanceActionInput) (*contracts.InstanceActionResult, error) {
	p.writes.Add(1)
	return &contracts.InstanceActionResult{Status: "running"}, nil
}
func (p *productionLockFixture) WithInstanceContext(context.Context) contracts.InstanceProvider {
	return p
}

func TestProductProductionLockMySQL(t *testing.T) {
	db := testenv.MySQL(t)
	tx := db.Begin()
	defer tx.Rollback()
	ctx := context.Background()
	sid := int64(994001)
	seedGateSupplier(t, tx, sid, 994002)
	expiry := time.Now().Add(time.Hour).Truncate(time.Millisecond)
	h := dom.Host{ID: 994003, InstanceNo: "PRODUCT-LOCK", CustomerID: 994004, ProductID: 994002, ModuleType: "cloud", ConfigSnapshot: `{"billingCycle":"monthly"}`, Status: 3, UpstreamProviderID: &sid, UpstreamInstanceID: "84042", OwnershipVersion: 1, ExpiresAt: &expiry}
	if err := tx.Create(&h).Error; err != nil {
		t.Fatal(err)
	}
	provider := &productionLockFixture{gateRenewalFixture: gateRenewalFixture{retail: "30.00", cost: "27.00", duration: 2678400, expiry: expiry}}
	s := NewService(repo.NewRepository(tx), securityResolver{provider: provider})
	s.db = tx
	if err := s.SetProductionLock(ctx, h.CustomerID, h.ID, true); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"start", "stop", "reboot", "hard_stop", "hard_reboot", "remote", "reinstall", "reset_password", "rescue", "exit_rescue", "destroy"} {
		_, err := s.Action(ctx, h.CustomerID, h.ID, ActionInput{Action: action})
		if !errors.Is(err, productionlock.ErrLocked) {
			t.Fatalf("%s bypass: %v", action, err)
		}
	}
	remark := "blocked"
	for name, err := range map[string]error{
		"remark":   s.SaveRemark(ctx, h.CustomerID, h.ID, RemarkInput{Remark: &remark}),
		"auto":     s.SetAutoRenew(ctx, h.CustomerID, h.ID, false),
		"transfer": pushtransfer.AvailableTx(tx, h.ID, 0, 0),
		"payment":  s.PayRenewalBalance(ctx, h.CustomerID, h.ID, 1, "127.0.0.1"),
	} {
		if !errors.Is(err, productionlock.ErrLocked) {
			t.Fatalf("%s bypass: %v", name, err)
		}
	}
	if _, err := s.CreateRemoteUpstream(ctx, h.ID, h.CustomerID); !errors.Is(err, productionlock.ErrLocked) {
		t.Fatal(err)
	}
	if _, err := s.ClaimRemoteSession(ctx, h.CustomerID, h.ID, "x"); !errors.Is(err, productionlock.ErrLocked) {
		t.Fatal(err)
	}
	if _, err := s.RemoteOwnership(ctx, h.ID, h.CustomerID); !errors.Is(err, productionlock.ErrLocked) {
		t.Fatal(err)
	}
	list, err := s.List(ctx, h.CustomerID, ListInput{})
	if err != nil || len(list.Items) != 1 || !list.Items[0].ProductionLocked {
		t.Fatalf("lock list: %+v %v", list, err)
	}
	if err := s.SetProductionLock(ctx, h.CustomerID+1, h.ID, false); !errors.Is(err, ErrForbidden) {
		t.Fatalf("foreign unlock %v", err)
	}
	if err := s.SetProductionLock(ctx, h.CustomerID, h.ID, false); err != nil {
		t.Fatal(err)
	}
	if provider.writes.Load() != 0 {
		t.Fatal("locked operation reached provider")
	}
	if _, err := s.Action(ctx, h.CustomerID, h.ID, ActionInput{Action: "reboot"}); err != nil {
		t.Fatal("unlocked server operation failed", err)
	}
	if provider.writes.Load() != 1 {
		t.Fatal("unlock did not restore server operations")
	}
	if err := s.SaveRemark(ctx, h.CustomerID, h.ID, RemarkInput{Remark: &remark}); err != nil {
		t.Fatal("unlock failed", err)
	}
	months := 12
	if err := tx.Create(&order.Order{ID: 994006, OrderNo: "ORIGINAL-PURCHASE-FIXTURE", CustomerID: h.CustomerID, TotalAmount: 12000, PayAmount: 12000, Status: 6}).Error; err != nil {
		t.Fatal(err)
	}
	if err := tx.Create(&order.OrderItem{ID: 994005, OrderID: 994006, ProductID: h.ProductID, ModuleType: "cloud", ConfigSnapshot: `{}`, DurationMonths: &months, Qty: 1}).Error; err != nil {
		t.Fatal(err)
	}
	if err := tx.Model(&dom.Host{}).Where("id=?", h.ID).Updates(map[string]any{"order_id": 994006, "order_item_id": 994005, "config_snapshot": `{"billingCycle":"monthly"}`}).Error; err != nil {
		t.Fatal(err)
	}
	if cycle, err := s.OriginalRenewalCycle(ctx, h.CustomerID, h.ID); err != nil || cycle != "annually" {
		t.Fatalf("original purchase year lost: %s %v", cycle, err)
	}
}

func TestProductAutoRenewalWindowAndInsufficientMySQL(t *testing.T) {
	db := testenv.MySQL(t)
	tx := db.Begin()
	defer tx.Rollback()
	ctx := context.Background()
	sid := int64(996001)
	seedGateSupplier(t, tx, sid, 996002)
	expiry := time.Now().Add(23 * time.Hour).Truncate(time.Millisecond)
	h := dom.Host{ID: 996003, InstanceNo: "PRODUCT-AUTO", CustomerID: 996004, ProductID: 996002, ModuleType: "cloud", ConfigSnapshot: `{"billingCycle":"monthly"}`, Status: 3, UpstreamProviderID: &sid, UpstreamInstanceID: "84042", OwnershipVersion: 1, ExpiresAt: &expiry}
	if e := tx.Exec("INSERT INTO customer.customers(id,username,password_hash) VALUES(996004,'product-controls-disabled','!login-disabled')").Error; e != nil {
		t.Fatal(e)
	}
	if e := tx.Create(&h).Error; e != nil {
		t.Fatal(e)
	}
	p := &gateRenewalFixture{retail: "30.00", cost: "27.00", duration: 2678400, expiry: expiry}
	s := NewService(repo.NewRepository(tx), securityResolver{provider: p})
	s.db = tx
	options, e := s.RenewalOptions(ctx, h.CustomerID, h.ID)
	if e != nil || len(options) != 1 {
		t.Fatal(e)
	}
	if e := s.SetAutoRenew(ctx, h.CustomerID, h.ID, true, options[0].QuoteRevision); e != nil {
		t.Fatal(e)
	}
	candidates, e := s.autoRenewalCandidates(ctx)
	if e != nil || len(candidates) != 1 {
		t.Fatalf("23h: %+v %v", candidates, e)
	}
	tx.Model(&dom.Host{}).Where("id=?", h.ID).Update("expires_at", time.Now().Add(25*time.Hour))
	candidates, e = s.autoRenewalCandidates(ctx)
	if e != nil || len(candidates) != 0 {
		t.Fatal("25h should not renew", e)
	}
	tx.Model(&dom.Host{}).Where("id=?", h.ID).Update("expires_at", expiry)
	s.autoRenewOnce(ctx, h, "monthly")
	s.autoRenewOnce(ctx, h, "monthly")
	var debits int64
	tx.Table("billing.transactions").Where("customer_id=? AND amount<0", h.CustomerID).Count(&debits)
	if debits != 0 {
		t.Fatal("insufficient balance was debited")
	}
	var attempts int64
	tx.Model(&ProductLifecycle{}).Where("instance_id=? AND kind='auto_renew_attempt'", h.ID).Count(&attempts)
	if attempts != 1 {
		t.Fatal("duplicate automatic attempt", attempts)
	}
	var messages int64
	tx.Model(&notification.Outbox{}).Where("payload LIKE ?", "%账户余额不足%").Count(&messages)
	if messages != 1 {
		t.Fatal("missing insufficient-balance notification", messages)
	}
	if err := s.SetAutoRenew(ctx, h.CustomerID, h.ID, false); err != nil {
		t.Fatal(err)
	}
	var unpaid Renewal
	if err := tx.Where("instance_id=?", h.ID).Take(&unpaid).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.PayRenewalBalance(context.WithValue(ctx, autoRenewPaymentKey{}, true), h.CustomerID, h.ID, unpaid.OrderID, "system_job"); err == nil {
		t.Fatal("disabled automatic renewal accepted payment")
	}
	if err := s.SetAutoRenew(ctx, h.CustomerID, h.ID, true, options[0].QuoteRevision); err != nil {
		t.Fatal(err)
	}
	if _, err := balance.New(tx).Change(ctx, balance.ChangeRequest{CustomerID: h.CustomerID, Type: "manual_credit", Amount: "100.00", RefType: "customer", RefID: h.CustomerID, Channel: "admin_manual", RequestID: "product-controls-test-fund", AdminID: 1, AdminName: "isolated", OperatorIP: "127.0.0.1", ClientNotes: "隔离测试资金", Notes: "isolated test funding"}); err != nil {
		t.Fatal(err)
	}
	paidHost := h
	tx.Model(&ProductLifecycle{}).Where("instance_id=? AND kind='auto_renew_attempt'", h.ID).Update("next_attempt_at", time.Now().Add(-time.Minute))
	s.autoRenewOnce(ctx, paidHost, "monthly")
	s.autoRenewOnce(ctx, paidHost, "monthly")
	tx.Table("billing.transactions").Where("customer_id=? AND amount<0", h.CustomerID).Count(&debits)
	if debits != 1 {
		t.Fatal("funded automatic payment was not exactly once", debits)
	}
	var account balance.Account
	if err := tx.Where("customer_id=?", h.CustomerID).Take(&account).Error; err != nil || account.Balance.String() != "70.00" {
		t.Fatalf("incorrect funded balance %s %v", account.Balance.String(), err)
	}
	var lifecycle ProductLifecycle
	if err := tx.Where("instance_id=? AND kind='auto_renew_attempt'", paidHost.ID).Take(&lifecycle).Error; err != nil || lifecycle.State != "paid" {
		t.Fatalf("automatic payment state %+v %v", lifecycle, err)
	}
}

type expiryFixture struct {
	gateRenewalFixture
	status       string
	reads        int
	fail         bool
	beforeReturn func()
}

func (p *expiryFixture) WithInstanceContext(context.Context) contracts.InstanceProvider { return p }
func (p *expiryFixture) GetInstanceDetail(id int64) (*contracts.InstanceDetail, error) {
	p.reads++
	if p.fail {
		return nil, errors.New("isolated read failure")
	}
	d, _ := p.gateRenewalFixture.GetInstanceDetail(id)
	d.Status = p.status
	d.PowerStatus = normalizePower(p.status)
	if p.beforeReturn != nil {
		f := p.beforeReturn
		p.beforeReturn = nil
		f()
	}
	return d, nil
}
func TestProductExpirySyncMySQL(t *testing.T) {
	db := testenv.MySQL(t)
	tx := db.Begin()
	defer tx.Rollback()
	ctx := context.Background()
	sid := int64(995001)
	seedGateSupplier(t, tx, sid, 995002)
	expiry := time.Now().Add(-time.Hour).Truncate(time.Millisecond)
	h := dom.Host{ID: 995003, InstanceNo: "PRODUCT-EXPIRY", CustomerID: 995004, ProductID: 995002, ModuleType: "cloud", ConfigSnapshot: `{"billingCycle":"monthly"}`, Status: 3, UpstreamProviderID: &sid, UpstreamInstanceID: "84042", OwnershipVersion: 1, ExpiresAt: &expiry}
	if err := tx.Create(&h).Error; err != nil {
		t.Fatal(err)
	}
	p := &expiryFixture{gateRenewalFixture: gateRenewalFixture{expiry: expiry}, status: "Active"}
	s := NewService(repo.NewRepository(tx), securityResolver{provider: p})
	s.db = tx
	s.expirySyncTick(ctx)
	var current dom.Host
	tx.Where("id=?", h.ID).Take(&current)
	if current.Status != 3 {
		t.Fatal("timestamp alone expired instance")
	}
	var event ProductLifecycle
	tx.Where("instance_id=?", h.ID).Take(&event)
	if event.State != "retry" || event.Attempts != 1 || event.LastError == "" {
		t.Fatalf("missing retry %+v", event)
	}
	p.status = "Suspended"
	tx.Model(&ProductLifecycle{}).Where("instance_id=?", h.ID).Update("next_attempt_at", time.Now().Add(-time.Minute))
	p.beforeReturn = func() {
		if err := tx.Model(&ProductLifecycle{}).Where("instance_id=? AND kind='expiry_sync'", h.ID).Updates(map[string]any{"attempts": 999, "state": "completed"}).Error; err != nil {
			t.Fatal(err)
		}
	}
	s.expirySyncTick(ctx)
	tx.Where("id=?", h.ID).Take(&current)
	if current.Status != 3 {
		t.Fatal("old lease response overwrote newer completed event")
	}
	tx.Where("instance_id=?", h.ID).Take(&event)
	if event.State != "completed" || event.Attempts != 999 {
		t.Fatal("old failure overwrote new completed lease")
	}
	tx.Model(&ProductLifecycle{}).Where("instance_id=?", h.ID).Updates(map[string]any{"state": "retry", "next_attempt_at": time.Now().Add(-time.Minute)})
	s.expirySyncTick(ctx)
	tx.Where("id=?", h.ID).Take(&current)
	if current.Status != 5 || current.PowerStatus != "suspended" {
		t.Fatalf("remote state not applied %+v", current)
	}
	reads := p.reads
	s.expirySyncTick(ctx)
	if p.reads != reads {
		t.Fatal("completed expiry event repeated")
	}
}
