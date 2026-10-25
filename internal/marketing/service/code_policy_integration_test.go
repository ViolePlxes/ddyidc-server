package marketing

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	dom "ddyidc/internal/marketing/domain"
	repo "ddyidc/internal/marketing/repository"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/internal/shared/testenv"
)

func TestPromoCodeGovernanceMySQL(t *testing.T) {
	db := testenv.MySQL(t)
	ctx := context.Background()
	repository := repo.NewRepository(db)
	s := NewService(db, repository)
	must := func(e error) {
		t.Helper()
		if e != nil {
			t.Fatal(e)
		}
	}
	must(repository.SaveConfig(ctx, CfgInviteEnabled, "1", "governance fixture"))
	must(repository.SaveConfig(ctx, CfgInviteReview, "0", "legacy bypass setting must be ignored"))
	owner := snowflake.GenID()
	other := owner + 1
	invitee := owner + 2
	must(db.Table("customer.customers").Create(map[string]any{"id": owner, "username": fmt.Sprintf("code-qa-%d", owner), "password_hash": "isolated", "status": 1}).Error)
	old := dom.Invite{UserID: owner, Code: "888888", Status: dom.InviteActive, InviteCount: 7}
	must(db.Create(&old).Error)
	relation := dom.InviteRelation{InviterID: owner, InviteeID: invitee, Level: 1, InviteCode: "888888", Status: 1}
	must(db.Create(&relation).Error)
	settled := time.Now().UTC()
	reward := dom.Reward{OrderID: owner, BizType: 1, UserID: invitee, RewardUserID: owner, Level: 1, RewardType: 1, BaseAmount: 10000, Amount: 1234, Status: dom.RewardSettled, SettledAt: &settled}
	must(db.Create(&reward).Error)
	t.Run("reclaim-retains-history-and-seals-old-code", func(t *testing.T) {
		if _, e := s.VerifyInviteCode(ctx, "888888"); e == nil {
			t.Fatal("reserved legacy code usable before scan")
		}
		must(s.ReclaimCode(ctx, old.ID, 99, "127.0.0.1", "quality code reclaim"))
		must(db.First(&old, old.ID).Error)
		if old.Code == "888888" || old.Status != 1 || old.InviteCount != 7 {
			t.Fatal("reclaim changed status/count or failed to replace")
		}
		var rel dom.InviteRelation
		must(db.First(&rel, relation.ID).Error)
		var r dom.Reward
		must(db.First(&r, reward.ID).Error)
		if rel.InviterID != owner || rel.InviteCode != "888888" || r.Amount != 1234 || r.Status != dom.RewardSettled {
			t.Fatal("history or earnings changed")
		}
		before := old.Code
		must(s.ReclaimCode(ctx, old.ID, 99, "127.0.0.1", "repeated reclaim"))
		must(db.First(&old, old.ID).Error)
		if old.Code != before {
			t.Fatal("reclaim not idempotent")
		}
		var n int64
		must(db.Table("marketing.code_audits").Where("user_id=? AND action='reclaim'", owner).Count(&n).Error)
		if n != 1 {
			t.Fatal("duplicate audit")
		}
	})
	candidate := fmt.Sprintf("ZK9D%05d", owner%100000)
	t.Run("custom-review-retains-current-code", func(t *testing.T) {
		v, e := s.ApplyInvite(ctx, owner, candidate)
		must(e)
		if v.Code != old.Code || v.Status != 1 || v.PendingCode == nil || *v.PendingCode != candidate {
			t.Fatal("custom request replaced working code or bypassed review")
		}
		if _, e = s.VerifyInviteCode(ctx, candidate); e == nil {
			t.Fatal("pending custom code usable")
		}
		if _, e = s.VerifyInviteCode(ctx, old.Code); e != nil {
			t.Fatal("working code unavailable while pending", e)
		}
		if _, e = s.ApplyInvite(ctx, other, candidate); e == nil {
			t.Fatal("pending claim stolen")
		}
		if e = s.GovernCode(ctx, old.ID, 99, "127.0.0.1", "approve", "verified content", "STALE"); e == nil {
			t.Fatal("stale review accepted")
		}
		must(s.GovernCode(ctx, old.ID, 99, "127.0.0.1", "approve", "verified content", candidate))
		if _, e = s.ApplyInvite(ctx, other, old.Code); e == nil {
			t.Fatal("retired system code reassigned")
		}
		must(db.First(&old, old.ID).Error)
		if old.Code != candidate || old.PendingCode != nil || old.InviteCount != 7 {
			t.Fatal("approval failed or history counter changed")
		}
	})
	t.Run("policy-update-blocks-approval-and-registration", func(t *testing.T) {
		next := fmt.Sprintf("QX8D%05d", owner%100000)
		_, e := s.ApplyInvite(ctx, owner, next)
		must(e)
		p, e := s.CodePolicy(ctx)
		must(e)
		p.ReservedCodes = append(p.ReservedCodes, candidate, next)
		must(s.SaveCodePolicy(ctx, p, 99, "127.0.0.1", "reserve platform codes"))
		if e = s.GovernCode(ctx, old.ID, 99, "127.0.0.1", "approve", "stale policy", next); e == nil {
			t.Fatal("new reserved rule bypassed")
		}
		if _, e = s.VerifyInviteCode(ctx, candidate); e == nil {
			t.Fatal("new reserved code usable before scan")
		}
		if e = s.SaveCodePolicy(ctx, p, 99, "127.0.0.1", "stale config"); e == nil {
			t.Fatal("stale policy accepted")
		}
		must(s.MaintainCodes(ctx))
		must(db.First(&old, old.ID).Error)
		if old.Code == candidate || old.PendingCode != nil {
			t.Fatal("scanner failed to rotate current/pending codes")
		}
		var rel dom.InviteRelation
		must(db.First(&rel, relation.ID).Error)
		if rel.InviterID != owner {
			t.Fatal("scanner changed attribution")
		}
	})
	t.Run("concurrent-custom-claim-has-one-owner", func(t *testing.T) {
		code := fmt.Sprintf("DX9K%05d", owner%100000)
		var wins atomic.Int32
		var wg sync.WaitGroup
		for i := int64(0); i < 2; i++ {
			wg.Add(1)
			go func(user int64) {
				defer wg.Done()
				if _, e := s.ApplyInvite(ctx, user, code); e == nil {
					wins.Add(1)
				}
			}(owner + 10 + i)
		}
		wg.Wait()
		if wins.Load() != 1 {
			t.Fatalf("claim winners %d", wins.Load())
		}
	})
	t.Run("registration-replay-keeps-history-and-counter", func(t *testing.T) {
		must(s.AfterRegister(ctx, invitee, "888888", ""))
		must(db.First(&old, old.ID).Error)
		if old.InviteCount != 7 {
			t.Fatal("historical replay incremented count")
		}
		if e := s.AfterRegister(ctx, owner+100, "888888", ""); e == nil {
			t.Fatal("retired code accepted for new customer")
		}
	})
	t.Run("runtime-grants-preserve-audit", func(t *testing.T) {
		pool, e := db.DB()
		must(e)
		if ApplyCodeRuntimePrivileges(ctx, pool, "root", "localhost") == nil {
			t.Fatal("unexpected identity admitted")
		}
		must(db.Exec("CREATE USER IF NOT EXISTS 'ddyidc_runtime'@'127.0.0.1' IDENTIFIED BY 'IsolatedCodeRuntime123!'").Error)
		must(ApplyCodeRuntimePrivileges(ctx, pool, "ddyidc_runtime", "127.0.0.1"))
		var forbidden int64
		must(db.Raw("SELECT COUNT(*) FROM information_schema.TABLE_PRIVILEGES WHERE GRANTEE=? AND TABLE_SCHEMA='marketing' AND TABLE_NAME IN ('code_policy','code_claims','code_audits') AND PRIVILEGE_TYPE IN ('DELETE','ALTER','DROP','CREATE')", "'ddyidc_runtime'@'127.0.0.1'").Scan(&forbidden).Error)
		if forbidden != 0 {
			t.Fatal("runtime destructive privileges granted")
		}
		must(db.Raw("SELECT COUNT(*) FROM information_schema.TABLE_PRIVILEGES WHERE GRANTEE=? AND TABLE_SCHEMA='marketing' AND TABLE_NAME='code_audits' AND PRIVILEGE_TYPE='UPDATE'", "'ddyidc_runtime'@'127.0.0.1'").Scan(&forbidden).Error)
		if forbidden != 0 {
			t.Fatal("runtime may rewrite audit")
		}
	})

	var funds int64
	must(db.Table("billing.transactions").Where("customer_id IN (?,?)", owner, invitee).Count(&funds).Error)
	if funds != 0 {
		t.Fatal("code governance moved funds")
	}
}
