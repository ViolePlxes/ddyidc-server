package admintodo

import (
	"context"
	dom "ddyidc/internal/marketing/domain"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/internal/shared/testenv"
	"fmt"
	"testing"
	"time"
)

func TestRegistrationInviteTodoMySQL(t *testing.T) {
	db := testenv.MySQL(t).Begin()
	defer db.Rollback()
	must := func(e error) {
		t.Helper()
		if e != nil {
			t.Fatal(e)
		}
	}
	ctx := context.Background()
	user := snowflake.GenID()
	must(db.Table("customer.customers").Create(map[string]any{"id": user, "tenant_id": 0, "username": fmt.Sprintf("invite-test-%d", user), "password_hash": "isolated", "status": 1, "created_at": time.Now(), "updated_at": time.Now()}).Error)
	row := dom.Invite{UserID: user, Code: fmt.Sprintf("T%d", user), Status: dom.InvitePending}
	must(db.Create(&row).Error)
	var pending dom.Invite
	must(db.First(&pending, row.ID).Error)
	page, e := List(ctx, db, "recommendations", Filter{Page: 1, PageSize: 20, CustomerID: fmt.Sprint(user)})
	must(e)
	if page.Total != 1 || page.Items[0].Source != "invite" || page.Items[0].Amount != "" {
		t.Fatal("registration queue used another reward system")
	}
	key := fmt.Sprintf("INVITE-%d", row.ID)
	must(ReviewInvite(ctx, db, row.ID, 1, "127.0.0.1", "approve", "checked coupon", key, pending.UpdatedAt))
	must(ReviewInvite(ctx, db, row.ID, 1, "127.0.0.1", "approve", "checked coupon", key, pending.UpdatedAt))
	if ReviewInvite(ctx, db, row.ID, 1, "127.0.0.1", "reject", "stale coupon", key+"-stale", pending.UpdatedAt) == nil {
		t.Fatal("stale review accepted")
	}
	page, e = List(ctx, db, "recommendations", Filter{Page: 1, PageSize: 20, CustomerID: fmt.Sprint(user)})
	must(e)
	if page.Total != 0 {
		t.Fatal("approved coupon remained pending")
	}
	page, e = List(ctx, db, "recommendations", Filter{Page: 1, PageSize: 20, CustomerID: fmt.Sprint(user), History: true})
	must(e)
	if page.Total != 1 || page.Items[0].Status != "1" {
		t.Fatal("coupon history missing")
	}
	var audits int64
	must(db.Table("system.todo_audits").Where("request_id=?", key).Count(&audits).Error)
	if audits != 1 {
		t.Fatal("review replay duplicated audit")
	}
	var funds, rewards int64
	must(db.Table("billing.transactions").Where("customer_id=?", user).Count(&funds).Error)
	must(db.Table("marketing.rewards").Where("reward_user_id=?", user).Count(&rewards).Error)
	if funds != 0 || rewards != 0 {
		t.Fatal("coupon review unexpectedly created money rewards")
	}
	rejectedUser := snowflake.GenID()
	must(db.Table("customer.customers").Create(map[string]any{"id": rejectedUser, "tenant_id": 0, "username": fmt.Sprintf("invite-reject-%d", rejectedUser), "password_hash": "isolated", "status": 1, "created_at": time.Now(), "updated_at": time.Now()}).Error)
	rejected := dom.Invite{UserID: rejectedUser, Code: fmt.Sprintf("R%d", user), Status: dom.InvitePending}
	must(db.Create(&rejected).Error)
	must(db.First(&rejected, rejected.ID).Error)
	must(ReviewInvite(ctx, db, rejected.ID, 1, "127.0.0.1", "reject", "invalid coupon", key+"-reject", rejected.UpdatedAt))
	must(db.First(&rejected, rejected.ID).Error)
	if rejected.Status != dom.InviteRejected || rejected.RejectReason == nil || *rejected.RejectReason != "invalid coupon" {
		t.Fatal("rejection evidence missing")
	}
	t.Log("existing registration coupon queue, approve/reject, stale conflict, audit replay and no balance credit verified; fixtures rolled back")
}
