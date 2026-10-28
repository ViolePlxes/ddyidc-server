package marketing

import (
	"context"
	dom "ddyidc/internal/marketing/domain"
	repo "ddyidc/internal/marketing/repository"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/internal/shared/testenv"
	"fmt"
	"testing"
	"time"
)

func TestCurrentUserDisplayMySQL(t *testing.T) {
	tx := testenv.MySQL(t).Begin()
	defer tx.Rollback()
	ctx := context.Background()
	repository := repo.NewRepository(tx)
	svc := NewService(tx, repository)
	owner, buyer, sales := snowflake.GenID(), snowflake.GenID(), snowflake.GenID()
	now := time.Now()
	alias := fmt.Sprintf("v10_%d", owner)
	for _, id := range []int64{owner, buyer} {
		if e := tx.Table("customer.customers").Create(map[string]any{"id": id, "username": fmt.Sprintf("v10_%d", id), "password_hash": "v10:unchanged-synthetic", "status": 1, "auth_version": 1, "created_at": now, "updated_at": now}).Error; e != nil {
			t.Fatal(e)
		}
	}
	if e := repository.SaveConfig(ctx, CfgInviteEnabled, "1", "isolated display test"); e != nil {
		t.Fatal(e)
	}
	code := "IDFIX7"
	invite := dom.Invite{ID: snowflake.GenID(), UserID: owner, Code: code, Status: dom.InviteActive, CreatedAt: now, UpdatedAt: now}
	if e := tx.Create(&invite).Error; e != nil {
		t.Fatal(e)
	}
	if e := claimCode(tx, code, owner, "issued"); e != nil {
		t.Fatal(e)
	}
	reward := dom.Reward{ID: snowflake.GenID(), OrderID: snowflake.GenID(), OrderNo: fmt.Sprint(owner), UserID: buyer, RewardUserID: owner, BizType: 1, Level: 1, RewardType: 2, BaseAmount: 10000, Amount: 123, Status: dom.RewardSettled, CreatedAt: now, UpdatedAt: now}
	if e := tx.Create(&reward).Error; e != nil {
		t.Fatal(e)
	}
	assignment := dom.SalesCustomer{ID: snowflake.GenID(), SalesID: sales, CustomerID: owner, Source: "admin", Status: 1, AssignedAt: now, CreatedAt: now, UpdatedAt: now}
	if e := tx.Create(&assignment).Error; e != nil {
		t.Fatal(e)
	}
	for _, keyword := range []string{fmt.Sprint(owner), alias} {
		rows, n, e := repository.ListInvites(ctx, 1, 20, nil, keyword)
		if e != nil || n != 1 || len(rows) != 1 || rows[0].Username != fmt.Sprint(owner) || rows[0].UserID != owner || rows[0].Code != code {
			t.Fatal("invite ID display/search/ownership failed")
		}
	}
	name, e := svc.VerifyInviteCode(ctx, code)
	if e != nil || name != fmt.Sprint(owner) {
		t.Fatal("verification leaked old account")
	}
	for _, keyword := range []string{fmt.Sprint(owner), fmt.Sprint(buyer)} {
		rows, n, e := repository.ListRewards(ctx, 1, 20, nil, nil, keyword, 0)
		if e != nil || n != 1 || len(rows) != 1 {
			t.Fatal("reward search failed")
		}
		v := rewardToDTO(rows[0])
		if v.Username != fmt.Sprint(buyer) || v.RewardUserName != fmt.Sprint(owner) || v.Amount != 123 || v.BaseAmount != 10000 || v.UserID != fmt.Sprint(buyer) || v.RewardUserID != fmt.Sprint(owner) || v.Status != dom.RewardSettled {
			t.Fatal("reward display or financial/owner fields changed")
		}
	}
	rows, n, e := repository.ListSalesCustomers(ctx, sales, 1, 20, fmt.Sprint(owner))
	if e != nil || n != 1 || len(rows) != 1 || rows[0].Username != fmt.Sprint(owner) || rows[0].CustomerID != owner {
		t.Fatal("sales customer ID display/search failed")
	}
	var stored struct{ Username, PasswordHash string }
	if e := tx.Table("customer.customers").Select("username,password_hash").Where("id=?", owner).Take(&stored).Error; e != nil || stored.Username != alias || stored.PasswordHash != "v10:unchanged-synthetic" {
		t.Fatal("login compatibility changed")
	}
	var after dom.Reward
	if e := tx.First(&after, "id=?", reward.ID).Error; e != nil || after.UserID != buyer || after.RewardUserID != owner || after.Amount != 123 || after.Status != reward.Status {
		t.Fatal("stored reward changed")
	}
	var bound dom.SalesCustomer
	if e := tx.First(&bound, "id=?", assignment.ID).Error; e != nil || bound.SalesID != sales || bound.CustomerID != owner || bound.Status != 1 {
		t.Fatal("stored ownership changed")
	}
}
