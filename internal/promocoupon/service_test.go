package promocoupon

import (
	"context"
	balance "ddyidc/internal/billing/balance"
	md "ddyidc/internal/marketing/domain"
	mr "ddyidc/internal/marketing/repository"
	ms "ddyidc/internal/marketing/service"
	od "ddyidc/internal/order/domain"
	"ddyidc/internal/points"
	"ddyidc/internal/shared/testenv"
	"encoding/json"
	"fmt"
	"gorm.io/gorm"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestQuoteIntegrity(t *testing.T) {
	s := New(nil, nil, []byte("isolated-quote-secret"))
	in := Input{TemplateID: 1, Source: "points", Mode: "direct", RecipientIDs: []string{"2"}, RequestID: "12345678-1234-1234-1234-123456789000"}
	if e := normalize(&in); e != nil {
		t.Fatal(e)
	}
	token := s.sign(quoteClaims{User: 1, Hash: fingerprint(in), Expires: s.now().Add(time.Minute).Unix()})
	if _, e := s.verify(token, 1, in); e != nil {
		t.Fatal(e)
	}
	if _, e := s.verify(token, 2, in); e == nil {
		t.Fatal("cross-account quote accepted")
	}
	changed := in
	changed.RecipientIDs = []string{"3"}
	if e := normalize(&changed); e != nil {
		t.Fatal(e)
	}
	if _, e := s.verify(token, 1, changed); e == nil {
		t.Fatal("recipient tampering accepted")
	}
	in.Source = "balance"
	if _, e := s.verify(token, 1, in); e == nil {
		t.Fatal("asset tampering accepted")
	}
	token = s.sign(quoteClaims{User: 1, Hash: fingerprint(in), Expires: s.now().Unix() - 1})
	if _, e := s.verify(token, 1, in); e == nil {
		t.Fatal("expired quote accepted")
	}
	c := Config{BatchMax: 100, FaceMax: 2000, ValidDaysMax: 30, PointsPerYuan: 1000}
	tpl := Template{Face: 100, Minimum: 1000, ValidDays: 30}
	in = Input{Source: "points", Mode: "offer", Quantity: 3, ClaimPoints: 250}
	p, b, e := costs(tpl, c, in)
	if e != nil || p != 2250 || b != 0 {
		t.Fatalf("split cost %d %v %v", p, b, e)
	}
	in.Source = "balance"
	if _, _, e = costs(tpl, c, in); e == nil {
		t.Fatal("mixed assets accepted")
	}
}
func TestPromoCouponsMySQL(t *testing.T) {
	db := testenv.MySQL(t)
	ctx := context.Background()
	p := points.NewService(db, nil)
	s := New(db, p, []byte("0123456789abcdef0123456789abcdef"))
	now := time.Now().Truncate(time.Second)
	s.now = func() time.Time { return now }
	const owner int64 = 9928801
	const child int64 = 9928802
	const stranger int64 = 9928803
	must := func(e error) {
		t.Helper()
		if e != nil {
			t.Fatal(e)
		}
	}
	for _, id := range []int64{owner, child, stranger} {
		must(db.Exec("INSERT INTO customer.customers(id,username,password_hash,status) VALUES(?,?,?,1)", id, fmt.Sprintf("promo-qa-%d", id), "not-a-login-password").Error)
		must(db.Table("system.points_accounts").Create(&points.Account{UserID: id, Balance: 100000, TotalEarned: 100000, Status: true}).Error)
		expiry := now.Add(time.Hour)
		must(db.Table("system.points_records").Create(&points.Record{UserID: id, Type: "gain", Source: "qa", Event: "qa", RefID: "original", Change: 100000, BalanceAfter: 100000, ExpireAt: &expiry, CreatedAt: now.Add(-time.Hour)}).Error)
	}
	must(db.Create(&md.InviteRelation{ID: 9928801, InviterID: owner, InviteeID: child, Level: 1, Status: 1}).Error)
	must(db.Table("system.points_settings").Where("id=1").Update("enabled", true).Error)
	must(db.Model(&Config{}).Where("id=1").Updates(map[string]any{"enabled": true, "review_required": false, "product_ids": `["77"]`, "daily_count": 100, "daily_budget": "200.00"}).Error)
	issue := func(source, mode string, ids []string, q int, claim int64) *Batch {
		t.Helper()
		in := Input{TemplateID: 1, Mode: mode, Source: source, RecipientIDs: ids, Quantity: q, ClaimPoints: claim, RequestID: fmt.Sprintf("qa-%020d", time.Now().UnixNano())}
		quote, e := s.Quote(ctx, owner, in)
		must(e)
		in.Quote = quote.Token
		b, e := s.Create(ctx, Actor{ID: owner, Role: "customer", Notes: "QA"}, in)
		must(e)
		again, e := s.Create(ctx, Actor{ID: owner, Role: "customer"}, in)
		must(e)
		if again.ID != b.ID {
			t.Fatal("duplicate batch")
		}
		b, e = s.Fund(ctx, Actor{ID: owner, Role: "customer", Notes: "QA"}, b.ID)
		must(e)
		return b
	}
	t.Run("points direct reclaim and original expiry", func(t *testing.T) {
		b := issue("points", "direct", []string{fmt.Sprint(child)}, 1, 0)
		a, e := p.Account(ctx, owner)
		must(e)
		if a.Balance != 99000 {
			t.Fatal("incorrect debit", a.Balance)
		}
		if _, e = s.Detail(ctx, stranger, b.ID, false); e == nil {
			t.Fatal("cross-owner detail")
		}
		must(s.Close(ctx, Actor{ID: owner, Role: "customer", Notes: "QA回收"}, b.ID))
		must(s.ProcessBatch(ctx, b.ID))
		must(s.ProcessBatch(ctx, b.ID))
		a, e = p.Account(ctx, owner)
		must(e)
		if a.Balance != 100000 {
			t.Fatal("refund mismatch", a.Balance)
		}
		var rows []points.Record
		must(db.Table("system.points_records").Where("user_id=? AND source='promo_coupon_refund'", owner).Find(&rows).Error)
		if len(rows) != 1 || rows[0].ExpireAt == nil || !rows[0].ExpireAt.Equal(now.Add(time.Hour)) {
			t.Fatal("refund refreshed expiry")
		}
	})
	t.Run("privacy relation and disabled gates", func(t *testing.T) {
		in := Input{TemplateID: 1, Mode: "direct", Source: "points", RecipientIDs: []string{fmt.Sprint(stranger)}, RequestID: "qa-forbidden-recipient"}
		if _, e := s.Quote(ctx, owner, in); e == nil {
			t.Fatal("stranger issued")
		}
		in.RecipientIDs = []string{fmt.Sprint(owner)}
		if _, e := s.Quote(ctx, owner, in); e == nil {
			t.Fatal("self issued")
		}
		must(db.Model(&Config{}).Where("id=1").Update("enabled", false).Error)
		in.RecipientIDs = []string{fmt.Sprint(child)}
		if _, e := s.Quote(ctx, owner, in); e == nil {
			t.Fatal("disabled purchase")
		}
		must(db.Model(&Config{}).Where("id=1").Update("enabled", true).Error)
	})
	t.Run("claim concurrency and split asset refunds", func(t *testing.T) {
		b := issue("points", "offer", nil, 1, 200)
		must(db.Create(&md.InviteRelation{ID: 9928802, InviterID: owner, InviteeID: stranger, Level: 1, Status: 1}).Error)
		var success atomic.Int32
		var wg sync.WaitGroup
		for i := 0; i < 20; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				user := child
				if i%2 == 1 {
					user = stranger
				}
				if _, e := s.Claim(ctx, Actor{ID: user, Role: "customer"}, b.ID, fmt.Sprintf("claim-%020d", i)); e == nil {
					success.Add(1)
				}
			}(i)
		}
		wg.Wait()
		var issued int64
		must(db.Model(&Unit{}).Where("batch_id=? AND status='claimed'", b.ID).Count(&issued).Error)
		if issued != 1 {
			t.Fatal("oversold", issued, success.Load())
		}
		must(s.Close(ctx, Actor{ID: owner, Role: "customer", Notes: "QA回收"}, b.ID))
		must(s.ProcessBatch(ctx, b.ID))
		for _, id := range []int64{owner, child, stranger} {
			a, e := p.Account(ctx, id)
			must(e)
			if a.Balance != 100000 {
				t.Fatal("split refund", id, a.Balance)
			}
		}
	})
	t.Run("balance ledger and duplicate payment", func(t *testing.T) {
		_, e := balance.New(db).Change(ctx, balance.ChangeRequest{CustomerID: owner, Type: "manual_credit", Amount: "20.00", RefType: "customer", RefID: owner, Channel: "admin_manual", RequestID: "promo-qa-credit", AdminID: 1, AdminName: "QA", OperatorIP: "127.0.0.1", ClientNotes: "测试客户可见原因", Notes: "隔离验收"})
		must(e)
		b := issue("balance", "direct", []string{fmt.Sprint(child)}, 1, 0)
		must(s.PayBalance(ctx, owner, b.OrderID))
		var a balance.Account
		must(db.Take(&a, "customer_id=?", owner).Error)
		if a.Balance != 1900 {
			t.Fatal("yuan mismatch", a.Balance)
		}
		must(s.Close(ctx, Actor{ID: owner, Role: "customer", Notes: "QA回收"}, b.ID))
		must(s.ProcessBatch(ctx, b.ID))
		must(s.ProcessBatch(ctx, b.ID))
		must(db.Take(&a, "customer_id=?", owner).Error)
		if a.Balance != 2000 {
			t.Fatal("balance refund mismatch", a.Balance)
		}
	})
	t.Run("lock cancel paid partial full refund expiry", func(t *testing.T) {
		b := issue("points", "direct", []string{fmt.Sprint(child)}, 1, 0)
		var u Unit
		must(db.Take(&u, "batch_id=?", b.ID).Error)
		marketing := ms.NewService(db, mr.NewRepository(db))
		marketing.SetFundedCouponHooks(s.GuardCouponTx, s.CouponChargedTx)
		marketing.SetFundedCouponRelease(s.CouponReleasedTx)
		order := od.Order{ID: 9928890, CustomerID: child, OrderNo: "QA-PC-USE", TotalAmount: 1000, PayAmount: 900, Status: 1}
		item := od.OrderItem{ID: 9928890, OrderID: order.ID, ProductID: 77, ProductName: "isolated", ModuleType: "cloud", ConfigSnapshot: "{}", Qty: 1, UnitPrice: 1000, Subtotal: 1000}
		must(db.Create(&order).Error)
		must(db.Create(&item).Error)
		must(db.Transaction(func(tx *gorm.DB) error {
			discount, e := marketing.ChargeCouponForOrder(ctx, tx, child, *u.UserCouponID, order.ID, 1000)
			if e == nil && discount != 100 {
				return fmt.Errorf("discount %d", discount)
			}
			return e
		}))
		must(s.Close(ctx, Actor{ID: owner, Role: "customer", Notes: "锁券回收"}, b.ID))
		must(s.ProcessBatch(ctx, b.ID))
		must(db.Take(&u, "id=?", u.ID).Error)
		if u.Status != "locked" {
			t.Fatal("locked refunded", u.Status)
		}
		must(db.Model(&order).Update("status", 6).Error)
		must(s.ProcessBatch(ctx, b.ID))
		must(db.Model(&order).Update("status", 5).Error)
		must(s.ProcessBatch(ctx, b.ID))
		must(db.Take(&u, "id=?", u.ID).Error)
		if u.Status != "used" {
			t.Fatal("partial restored", u.Status)
		}
		must(db.Model(&order).Update("status", 4).Error)
		must(s.ProcessBatch(ctx, b.ID))
		must(db.Take(&u, "id=?", u.ID).Error)
		if u.Status != "refunded" {
			t.Fatal("full refund closing", u.Status)
		}
	})
	t.Run("twenty customers one stock", func(t *testing.T) {
		b := issue("points", "offer", nil, 1, 100)
		var wg sync.WaitGroup
		var wins atomic.Int32
		for i := 0; i < 20; i++ {
			id := int64(9929000 + i)
			must(db.Exec("INSERT INTO customer.customers(id,username,password_hash,status) VALUES(?,?,?,1)", id, fmt.Sprintf("race-%d", id), "not-a-password").Error)
			must(db.Create(&md.InviteRelation{ID: id, InviterID: owner, InviteeID: id, Level: 1, Status: 1}).Error)
			must(db.Table("system.points_accounts").Create(&points.Account{UserID: id, Balance: 200, TotalEarned: 200, Status: true}).Error)
			must(db.Table("system.points_records").Create(&points.Record{UserID: id, Type: "gain", Source: "qa", Event: "qa", RefID: "one-stock", Change: 200, BalanceAfter: 200, CreatedAt: now}).Error)
		}
		for i := 0; i < 20; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				if _, e := s.Claim(ctx, Actor{ID: int64(9929000 + i), Role: "customer"}, b.ID, fmt.Sprintf("race-%020d", i)); e == nil {
					wins.Add(1)
				}
			}(i)
		}
		wg.Wait()
		if wins.Load() != 1 {
			t.Fatal("stock winners", wins.Load())
		}
		var net int64
		must(db.Table("system.points_records").Where("user_id BETWEEN 9929000 AND 9929019 AND source='promo_coupon'").Select("COALESCE(SUM(change_value),0)").Scan(&net).Error)
		if net != -100 {
			t.Fatal("loser debited", net)
		}
		must(s.Close(ctx, Actor{ID: owner, Role: "customer", Notes: "QA回收"}, b.ID))
		must(s.ProcessBatch(ctx, b.ID))
	})
	t.Run("approval funding failure and recovery", func(t *testing.T) {
		must(db.Model(&Config{}).Where("id=1").Update("review_required", true).Error)
		in := Input{TemplateID: 1, Mode: "direct", Source: "points", RecipientIDs: []string{fmt.Sprint(child)}, RequestID: "qa-review-and-failure"}
		q, e := s.Quote(ctx, owner, in)
		must(e)
		in.Quote = q.Token
		b, e := s.Create(ctx, Actor{ID: owner, Role: "customer", Notes: "QA"}, in)
		must(e)
		before, e := p.Account(ctx, owner)
		must(e)
		if _, e = s.Fund(ctx, Actor{ID: owner, Role: "customer"}, b.ID); e == nil {
			t.Fatal("unreviewed funded")
		}
		must(s.Review(ctx, Actor{ID: 1, Role: "admin", Notes: "隔离通过"}, b.ID, true))
		must(db.Exec("CREATE TRIGGER marketing.qa_promo_issue_failure BEFORE INSERT ON marketing.user_coupons FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='isolated injected failure'").Error)
		if _, e = s.Fund(ctx, Actor{ID: owner, Role: "customer", Notes: "QA"}, b.ID); e == nil {
			t.Fatal("fault did not fail")
		}
		must(db.Exec("DROP TRIGGER marketing.qa_promo_issue_failure").Error)
		after, e := p.Account(ctx, owner)
		must(e)
		if after.Balance != before.Balance {
			t.Fatal("rolled back debit missing")
		}
		b, e = s.Fund(ctx, Actor{ID: owner, Role: "customer", Notes: "QA"}, b.ID)
		must(e)
		must(s.Close(ctx, Actor{ID: owner, Role: "customer", Notes: "回收"}, b.ID))
		must(s.ProcessBatch(ctx, b.ID))
		after, e = p.Account(ctx, owner)
		must(e)
		if after.Balance != before.Balance {
			t.Fatal("recovery amount mismatch")
		}
		must(db.Model(&Config{}).Where("id=1").Update("review_required", false).Error)
	})
	t.Run("renewal and scenario boundaries", func(t *testing.T) {
		must(db.Model(&Config{}).Where("id=1").Update("renewal_enabled", true).Error)
		b := issue("points", "direct", []string{fmt.Sprint(child)}, 1, 0)
		var u Unit
		must(db.Take(&u, "batch_id=?", b.ID).Error)
		marketing := ms.NewService(db, mr.NewRepository(db))
		marketing.SetFundedCouponHooks(s.GuardCouponTx, s.CouponChargedTx)
		s.SetCouponApply(marketing.ChargeCouponForOrder)
		order := od.Order{ID: 9928891, CustomerID: child, OrderNo: "QA-PC-RENEW", TotalAmount: 1000, PayAmount: 1000, Status: 1}
		must(db.Create(&order).Error)
		must(db.Create(&od.OrderItem{ID: 9928891, OrderID: order.ID, ProductID: 77, ProductName: "renewal", ModuleType: "instance_upgrade", ConfigSnapshot: "{}", Qty: 1, UnitPrice: 1000, Subtotal: 1000}).Error)
		if e := s.ApplyOrderCoupon(ctx, Actor{ID: child, Role: "customer", Notes: "QA"}, order.ID, *u.UserCouponID); e == nil {
			t.Fatal("upgrade discounted")
		}
		must(db.Model(&od.OrderItem{}).Where("id=?", 9928891).Update("module_type", "instance_renewal").Error)
		must(db.Exec("INSERT INTO instance.instance_renewals(id,order_id,instance_id,customer_id,supplier_id,host_id,cycle,duration,amount,cost,base_expiry,state,created_at,updated_at) VALUES(9928891,?,1,?,1,'QA','monthly',1,10.00,8.00,NOW(),'unpaid',NOW(),NOW())", order.ID, child).Error)
		must(s.ApplyOrderCoupon(ctx, Actor{ID: child, Role: "customer", Notes: "QA"}, order.ID, *u.UserCouponID))
		must(s.ApplyOrderCoupon(ctx, Actor{ID: child, Role: "customer", Notes: "QA"}, order.ID, *u.UserCouponID))
		must(db.Take(&order, "id=?", order.ID).Error)
		if order.PayAmount != 900 || order.CouponAmount != 100 {
			t.Fatal("renewal amount", order.PayAmount)
		}
		var amount string
		must(db.Table("instance.instance_renewals").Where("order_id=?", order.ID).Select("amount").Scan(&amount).Error)
		if amount != "9.00" {
			t.Fatal("renewal payable", amount)
		}
		must(db.Model(&order).Update("status", 3).Error)
		must(s.ProcessBatch(ctx, b.ID))
		must(s.Close(ctx, Actor{ID: owner, Role: "customer", Notes: "回收"}, b.ID))
		must(s.ProcessBatch(ctx, b.ID))
		must(db.Model(&Config{}).Where("id=1").Update("renewal_enabled", false).Error)
	})
	t.Run("permissions and private claim bypass", func(t *testing.T) {
		must(db.Exec("INSERT INTO system.admins(id,username,password_hash,real_name,status,is_super) VALUES(9928881,'promo-subadmin','not-a-password','QA',1,0)").Error)
		ok, e := s.Allowed(ctx, 9928881, "view")
		must(e)
		if ok {
			t.Fatal("ungranted admin access")
		}
		must(db.Create(&Permission{AdminID: 9928881, CanView: true}).Error)
		ok, e = s.Allowed(ctx, 9928881, "view")
		must(e)
		if !ok {
			t.Fatal("view denied")
		}
		ok, e = s.Allowed(ctx, 9928881, "review")
		must(e)
		if ok {
			t.Fatal("review permission bypass")
		}
		b := issue("points", "offer", nil, 1, 0)
		marketing := ms.NewService(db, mr.NewRepository(db))
		if _, e := marketing.ClaimCoupon(ctx, stranger, b.CouponID, "", 1); e == nil {
			t.Fatal("public claim private coupon")
		}
		must(s.Close(ctx, Actor{ID: owner, Role: "customer", Notes: "回收"}, b.ID))
		must(s.ProcessBatch(ctx, b.ID))
	})
	t.Run("insufficient funding stale quote and budget boundaries", func(t *testing.T) {
		must(db.Model(&Config{}).Where("id=1").Updates(map[string]any{"daily_count": 1000, "daily_budget": "10000.00"}).Error)
		in := Input{TemplateID: 1, Mode: "offer", Source: "balance", Quantity: 100, RequestID: "qa-insufficient-balance"}
		q, e := s.Quote(ctx, owner, in)
		must(e)
		in.Quote = q.Token
		b, e := s.Create(ctx, Actor{ID: owner, Role: "customer"}, in)
		must(e)
		if _, e = s.Fund(ctx, Actor{ID: owner, Role: "customer"}, b.ID); e == nil {
			t.Fatal("insufficient balance funded")
		}
		var n int64
		must(db.Model(&Unit{}).Where("batch_id=?", b.ID).Count(&n).Error)
		if n != 0 {
			t.Fatal("failed funding issued rights")
		}
		must(s.Close(ctx, Actor{ID: owner, Role: "customer", Notes: "QA取消"}, b.ID))
		in.RequestID = "qa-stale-rules-quote"
		in.Quantity = 1
		q, e = s.Quote(ctx, owner, in)
		must(e)
		in.Quote = q.Token
		must(db.Model(&Config{}).Where("id=1").UpdateColumn("version", gorm.Expr("version+1")).Error)
		if _, e = s.Create(ctx, Actor{ID: owner, Role: "customer"}, in); e == nil {
			t.Fatal("stale config quote accepted")
		}
		must(db.Model(&Config{}).Where("id=1").Update("daily_count", 1).Error)
		in.RequestID = "qa-daily-budget-limit"
		q, e = s.Quote(ctx, owner, in)
		must(e)
		in.Quote = q.Token
		if _, e = s.Create(ctx, Actor{ID: owner, Role: "customer"}, in); e == nil {
			t.Fatal("daily limit bypass")
		}
		must(db.Model(&Config{}).Where("id=1").Updates(map[string]any{"daily_count": 100, "daily_budget": "200.00"}).Error)
	})
	t.Run("maintenance retry disabled drain and notification recovery", func(t *testing.T) {
		b := issue("points", "direct", []string{fmt.Sprint(child)}, 1, 0)
		must(s.Close(ctx, Actor{ID: owner, Role: "customer", Notes: "QA重试"}, b.ID))
		must(s.Close(ctx, Actor{ID: owner, Role: "customer", Notes: "QA重复"}, b.ID))
		must(db.Model(&Config{}).Where("id=1").Update("enabled", false).Error)
		must(db.Exec("CREATE TRIGGER marketing.qa_promo_refund_failure BEFORE UPDATE ON marketing.promo_coupon_units FOR EACH ROW BEGIN IF NEW.status='refunded' THEN SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='QA transient refund failure'; END IF; END").Error)
		must(s.Tick(ctx))
		var task Task
		must(db.Take(&task, "task_key=?", fmt.Sprintf("close:%d", b.ID)).Error)
		if task.Attempts != 1 || task.Status != "pending" {
			t.Fatal("missing retry backoff", task)
		}
		must(db.Exec("DROP TRIGGER marketing.qa_promo_refund_failure").Error)
		must(db.Model(&Task{}).Where("id=?", task.ID).Updates(map[string]any{"status": "failed", "attempts": 5}).Error)
		must(s.Retry(ctx, Actor{ID: 1, Role: "admin", Notes: "QA修复后重试"}, task.ID))
		must(s.Tick(ctx))
		var unit Unit
		must(db.Take(&unit, "batch_id=?", b.ID).Error)
		if unit.Status != "refunded" {
			t.Fatal("disabled module did not drain", unit.Status)
		}
		must(s.Tick(ctx))
		must(db.Model(&Config{}).Where("id=1").Update("enabled", true).Error)
		pending := s.notice(db, 999992, child, "QA消息", "故障恢复测试")
		must(pending)
		must(db.Exec("CREATE TRIGGER common.qa_promo_notice_failure BEFORE INSERT ON common.notifications FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='QA notification failure'").Error)
		if e := s.DeliverNotices(ctx); e == nil {
			t.Fatal("notice failure hidden")
		}
		must(db.Exec("DROP TRIGGER common.qa_promo_notice_failure").Error)
		must(s.DeliverNotices(ctx))
		must(s.DeliverNotices(ctx))
		var notices int64
		must(db.Table("common.notifications").Where("template_code='promo_coupon' AND JSON_UNQUOTE(JSON_EXTRACT(params,'$.batchId'))='999992'").Count(&notices).Error)
		if notices != 1 {
			t.Fatal("notice duplicate", notices)
		}
	})
	t.Run("ledger sequence survives timezone timestamp disorder", func(t *testing.T) {
		must(db.Table("system.points_records").Where("user_id=? AND source='qa' AND ref_id='original'", owner).Update("created_at", now.AddDate(0, 0, 1)).Error)
		b := issue("points", "direct", []string{fmt.Sprint(child)}, 1, 0)
		must(s.Close(ctx, Actor{ID: owner, Role: "customer", Notes: "QA乱序回收"}, b.ID))
		must(s.ProcessBatch(ctx, b.ID))
		a, e := p.Account(ctx, owner)
		must(e)
		if a.Balance != 100000 {
			t.Fatal("timestamp ordering lost original lot", a.Balance)
		}
		must(db.Table("system.points_records").Where("user_id=? AND source='qa' AND ref_id='original'", owner).Update("created_at", now.Add(-time.Hour)).Error)
	})
	t.Run("expired points never become spendable", func(t *testing.T) {
		b := issue("points", "direct", []string{fmt.Sprint(child)}, 1, 0)
		must(db.Table("system.points_records").Where("user_id=? AND change_value>0", owner).Update("expire_at", time.Now().Add(-time.Hour)).Error)
		must(s.Close(ctx, Actor{ID: owner, Role: "customer", Notes: "到期积分回收"}, b.ID))
		must(s.ProcessBatch(ctx, b.ID))
		a, e := p.Account(ctx, owner)
		must(e)
		if a.Balance != 0 {
			t.Fatal("expired points revived", a.Balance)
		}
	})
	must(s.DeliverNotices(ctx))
	must(s.Reconcile(ctx))
	var failed int64
	must(db.Table("marketing.promo_coupon_reconciliations").Where("healthy=0").Count(&failed).Error)
	if failed > 0 {
		t.Fatal("budget reconciliation failed", failed)
	}
	detail, err := s.Detail(ctx, owner, func() int64 { var b Batch; must(db.Order("id").First(&b).Error); return b.ID }(), false)
	must(err)
	encoded, _ := json.Marshal(detail)
	t.Logf("financial lifecycle, privacy, concurrency and reconciliation passed; detail bytes=%d", len(encoded))
}
