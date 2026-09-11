package points

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"ddyidc/pkg/db"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

func TestPointsIntegration(t *testing.T) {
	if os.Getenv("DDYIDC_POINTS_QA_CLEAN") == "1" {
		database := openQA(t)
		cleanupBrowser(context.Background(), database)
		return
	}
	if os.Getenv("DDYIDC_POINTS_INTEGRATION") != "1" {
		t.Skip("set DDYIDC_POINTS_INTEGRATION=1")
	}
	ctx := context.Background()
	database := openQA(t)
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:6379"})
	defer rdb.Close()
	s := NewService(database, rdb)
	if err := s.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	var old bool
	if err := database.Table(settingsTable).Select("enabled").Where("id=1").Scan(&old).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Table(settingsTable).Where("id=1").Updates(map[string]any{"enabled": true, "signin_mode": "normal", "signin_stable_points": 5}).Error; err != nil {
		t.Fatal(err)
	}
	const userID int64 = 9910001
	cleanupCore(ctx, database, userID)
	defer func() {
		cleanupCore(ctx, database, userID)
		if os.Getenv("DDYIDC_POINTS_QA_PERSIST") != "1" {
			_ = database.Table(settingsTable).Where("id=1").Update("enabled", old).Error
		}
	}()
	const coreEvent = "qa_core_login_event"
	rule := Rule{ID: 9910001, Name: "QA 登录", Code: "qa_core_login", Event: coreEvent, Type: "fixed", PointsFixed: 20, LimitType: "day", LimitValue: 1, Enabled: true, LadderConfig: json.RawMessage(`[]`)}
	if err := database.Table(rulesTable).Create(&rule).Error; err != nil {
		t.Fatal(err)
	}
	task := Task{ID: 9910001, Name: "QA 登录任务", Code: "qa_core_task", TargetEvent: coreEvent, TaskType: "daily", Category: "daily", Points: 10, TargetValue: 1, Enabled: true}
	if err := database.Table(tasksTable).Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	_ = rdb.Del(ctx, fmt.Sprintf("points:limit:%d:%d:%s", rule.ID, userID, frequencyKey(rule.LimitType, s.now()))).Err()
	if got, err := s.GrantByEvent(ctx, coreEvent, userID, EventMeta{RefID: "qa-login"}); err != nil || got != 20 {
		t.Fatalf("grant got=%d err=%v", got, err)
	}
	if _, err := s.ClaimTask(ctx, userID, task.ID); err != nil {
		t.Fatal(err)
	}
	var success atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.Signin(ctx, userID, "normal"); err == nil {
				success.Add(1)
			}
		}()
	}
	wg.Wait()
	if success.Load() != 1 {
		t.Fatalf("concurrent signin success=%d", success.Load())
	}
	goods := Goods{ID: 9910001, Name: "QA 虚拟商品", Type: "virtual", Description: "QA", PointsCost: 15, Stock: 1, Status: true, Config: json.RawMessage(`{"content":"QA-CONTENT"}`)}
	if err := database.Table(goodsTable).Create(&goods).Error; err != nil {
		t.Fatal(err)
	}
	var err error
	var exchangeSuccess atomic.Int32
	exchanged := make(chan *Order, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if row, exchangeErr := s.Exchange(ctx, userID, goods.ID, 1, json.RawMessage(`{}`)); exchangeErr == nil {
				exchangeSuccess.Add(1)
				exchanged <- row
			}
		}()
	}
	wg.Wait()
	if exchangeSuccess.Load() != 1 {
		t.Fatalf("concurrent exchange success=%d", exchangeSuccess.Load())
	}
	order := <-exchanged
	if order.Status != "completed" || order.DeliveryContent != "QA-CONTENT" {
		t.Fatalf("unexpected order %#v", order)
	}
	if _, err = s.Order(ctx, userID+1, order.ID, false); !errors.Is(err, ErrForbidden) {
		t.Fatalf("expected cross-user forbidden, got %v", err)
	}
	if _, err = s.Exchange(ctx, userID, goods.ID, 1, json.RawMessage(`{}`)); err == nil || !strings.Contains(err.Error(), "库存") {
		t.Fatalf("expected stock protection, got %v", err)
	}
	riskTag := RiskTag{ID: 9910001, Name: "QA 中风险", Level: 2, Color: "orange", Description: "QA"}
	if err = database.Table(riskTagsTable).Create(&riskTag).Error; err != nil {
		t.Fatal(err)
	}
	riskRule := RiskRule{ID: 9910001, Name: "QA 七日退款", TagID: riskTag.ID, Conditions: json.RawMessage(`[{"metric":"refund_count","operator":">=","value":1,"windowDays":7}]`), Enabled: true}
	if err = database.Table(riskRulesTable).Create(&riskRule).Error; err != nil {
		t.Fatal(err)
	}
	err = database.Transaction(func(tx *gorm.DB) error {
		return s.creditTx(ctx, tx, userID, 40, "rule:qa_refund", EventOrder, "qa-refund", "QA 订单赠送")
	})
	if err != nil {
		t.Fatal(err)
	}
	if deducted, refundErr := s.RefundDeduct(ctx, userID, EventOrder, "qa-refund", 1); refundErr != nil || deducted != 40 {
		t.Fatalf("refund deducted=%d err=%v", deducted, refundErr)
	}
	var userTag UserTag
	if err = database.Table(userTagsTable).Where("user_id=?", userID).Take(&userTag).Error; err != nil || userTag.TagID != riskTag.ID || userTag.Source != "auto" {
		t.Fatalf("automatic risk tag=%#v err=%v", userTag, err)
	}
	if err = s.SetUserTag(ctx, userID, riskTag.ID, 1, true, "QA 手动锁定"); err != nil {
		t.Fatal(err)
	}
	if err = s.EvaluateRisk(ctx, userID); err != nil {
		t.Fatal(err)
	}
	if err = database.Table(userTagsTable).Where("user_id=?", userID).Take(&userTag).Error; err != nil || !userTag.IsLocked || userTag.Source != "manual" {
		t.Fatalf("locked risk tag=%#v err=%v", userTag, err)
	}
	if os.Getenv("DDYIDC_POINTS_QA_PERSIST") == "1" {
		cleanupBrowser(ctx, database)
		seedBrowser(t, ctx, database)
		t.Log("POINTS_QA_BROWSER_READY")
	}
}

func openQA(t *testing.T) *gorm.DB {
	t.Helper()
	if err := db.Init(db.Config{Host: "127.0.0.1", Port: 3306, User: "root", Password: "ddyidc123", Database: "ddyidc", MaxOpenConns: 20, MaxIdleConns: 5, ConnMaxLifetime: 3600}); err != nil {
		t.Fatal(err)
	}
	return db.G
}

func cleanupCore(ctx context.Context, database *gorm.DB, userID int64) {
	database.WithContext(ctx).Exec("DELETE FROM system.points_risk_logs WHERE user_id=?", userID)
	database.WithContext(ctx).Exec("DELETE FROM system.points_user_tags WHERE user_id=?", userID)
	database.WithContext(ctx).Exec("DELETE FROM system.points_risk_rules WHERE id BETWEEN 9910000 AND 9919999")
	database.WithContext(ctx).Exec("DELETE FROM system.points_risk_tags WHERE id BETWEEN 9910000 AND 9919999")
	database.WithContext(ctx).Exec("DELETE FROM system.points_orders WHERE id BETWEEN 9910000 AND 9919999 OR user_id=?", userID)
	database.WithContext(ctx).Exec("DELETE FROM system.points_signins WHERE user_id=?", userID)
	database.WithContext(ctx).Exec("DELETE FROM system.points_records WHERE user_id=?", userID)
	database.WithContext(ctx).Exec("DELETE FROM system.points_task_progress WHERE user_id=? OR task_id BETWEEN 9910000 AND 9919999", userID)
	database.WithContext(ctx).Exec("DELETE FROM system.points_accounts WHERE user_id=?", userID)
	database.WithContext(ctx).Exec("DELETE FROM system.points_goods WHERE id BETWEEN 9910000 AND 9919999")
	database.WithContext(ctx).Exec("DELETE FROM system.points_tasks WHERE id BETWEEN 9910000 AND 9919999")
	database.WithContext(ctx).Exec("DELETE FROM system.points_rules WHERE id BETWEEN 9910000 AND 9919999")
}
func seedBrowser(t *testing.T, ctx context.Context, database *gorm.DB) {
	t.Helper()
	var userID int64
	if err := database.WithContext(ctx).Table("customer.customers").Where("username IN ?", []string{"e2eqk09091047", "e2ea074640"}).Order("FIELD(username,'e2eqk09091047','e2ea074640')").Pluck("id", &userID).Error; err != nil || userID == 0 {
		t.Fatalf("browser QA user missing: %v", err)
	}
	database.Exec("INSERT INTO system.points_accounts (user_id,balance,total_earned,status) VALUES (?,500,500,1) ON DUPLICATE KEY UPDATE balance=500,frozen=0,total_earned=500,total_used=0,status=1", userID)
	database.Exec("INSERT INTO system.points_rules (id,name,code,event,type,points_fixed,points_min,points_max,ratio,ladder_config,limit_type,limit_value,enabled) VALUES (990001,'[验收]每日登录','qa_daily_login','login','fixed',5,0,0,0,JSON_ARRAY(),'day',2,1)")
	database.Exec("INSERT INTO system.points_tasks (id,name,code,description,target_event,task_type,category,points,target_value,enabled,sort) VALUES (990001,'[验收]每日签到','qa_signin','完成一次签到','signin','daily','daily',20,1,1,100),(990002,'[验收]累计登录','qa_login','本周登录两次','login','weekly','growth',50,2,1,90)")
	database.Exec("INSERT INTO system.points_goods_categories (id,parent_id,name,sort) VALUES (990001,0,'[验收]精选',100)")
	database.Exec("INSERT INTO system.points_goods (id,category_id,name,type,image,description,points_cost,stock,sold,status,config,is_hot,sort) VALUES (990001,990001,'[验收]虚拟权益','virtual','','[验收]自动发放虚拟内容',100,-1,0,1,JSON_OBJECT('content','QA-VIRTUAL-CONTENT'),1,100),(990002,990001,'[验收]卡密','card','','[验收]自动发放卡密',120,1,0,1,JSON_OBJECT(),0,90)")
	database.Exec("INSERT INTO system.points_card_batches (id,goods_id,total,used) VALUES (990001,990002,1,0)")
	database.Exec("INSERT INTO system.points_cards (id,batch_id,goods_id,card_value,used) VALUES (990001,990001,990002,'QA-CARD-ONLY',0)")
	database.Table(settingsTable).Where("id=1").Updates(map[string]any{"enabled": true, "signin_mode": "choose", "show_mall": true, "show_signin": true, "show_tasks": true})
	t.Logf("browser user id=%d", userID)
}
func cleanupBrowser(ctx context.Context, database *gorm.DB) {
	var userIDs []int64
	database.WithContext(ctx).Table("customer.customers").Where("username IN ?", []string{"e2eqk09091047", "e2ea074640"}).Pluck("id", &userIDs)
	if len(userIDs) > 0 {
		database.WithContext(ctx).Table(riskLogsTable).Where("user_id IN ?", userIDs).Delete(&RiskLog{})
		database.WithContext(ctx).Table(userTagsTable).Where("user_id IN ?", userIDs).Delete(&UserTag{})
		database.WithContext(ctx).Table(signinsTable).Where("user_id IN ?", userIDs).Delete(&Signin{})
		database.WithContext(ctx).Table(recordsTable).Where("user_id IN ?", userIDs).Delete(&Record{})
		database.WithContext(ctx).Table(accountsTable).Where("user_id IN ?", userIDs).Delete(&Account{})
	}
	database.WithContext(ctx).Exec("DELETE FROM system.points_orders WHERE goods_id BETWEEN 990000 AND 990999")
	database.WithContext(ctx).Exec("DELETE FROM system.points_cards WHERE id BETWEEN 990000 AND 990999")
	database.WithContext(ctx).Exec("DELETE FROM system.points_card_batches WHERE id BETWEEN 990000 AND 990999")
	database.WithContext(ctx).Exec("DELETE FROM system.points_goods WHERE id BETWEEN 990000 AND 990999")
	database.WithContext(ctx).Exec("DELETE FROM system.points_goods_categories WHERE id BETWEEN 990000 AND 990999")
	database.WithContext(ctx).Exec("DELETE FROM system.points_task_progress WHERE task_id BETWEEN 990000 AND 990999")
	database.WithContext(ctx).Exec("DELETE FROM system.points_tasks WHERE id BETWEEN 990000 AND 990999")
	database.WithContext(ctx).Exec("DELETE FROM system.points_rules WHERE id BETWEEN 990000 AND 990999")
	database.WithContext(ctx).Table(settingsTable).Where("id=1").Update("enabled", false)
}
