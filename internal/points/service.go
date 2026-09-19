package points

import (
	"context"
	"crypto/rand"
	balance "ddyidc/internal/billing/balance"
	"ddyidc/internal/shared/distsync"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	mathrand "math/rand"
	"strconv"
	"strings"
	"time"

	"ddyidc/internal/shared/snowflake"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrNotFound  = errors.New("记录不存在")
	ErrForbidden = errors.New("无权访问该记录")
	ErrDisabled  = errors.New("积分中心未开启")
)

//go:embed schema.sql
var schemaSQL string

type EventMeta struct {
	RefID       string
	AmountCents int64
	Remark      string
}
type Service struct {
	db     *gorm.DB
	redis  *redis.Client
	now    func() time.Time
	random *mathrand.Rand
}

func NewService(database *gorm.DB, redisClient *redis.Client) *Service {
	return &Service{db: database, redis: redisClient, now: time.Now, random: mathrand.New(mathrand.NewSource(time.Now().UnixNano()))}
}
func (s *Service) EnsureSchema(ctx context.Context) error {
	return s.db.WithContext(ctx).Exec(schemaSQL).Error
}
func (s *Service) Enabled(ctx context.Context) (bool, error) {
	var v bool
	e := s.db.WithContext(ctx).Table(settingsTable).Select("enabled").Where("id=1").Scan(&v).Error
	return v, e
}
func (s *Service) Settings(ctx context.Context) (*Settings, error) {
	var v Settings
	e := s.db.WithContext(ctx).Table(settingsTable).Where("id=1").Take(&v).Error
	return &v, e
}
func (s *Service) SaveSettings(ctx context.Context, v *Settings) error {
	if v.SigninLuckyMin < 0 || v.SigninLuckyMax < v.SigninLuckyMin {
		return errors.New("幸运签到积分范围无效")
	}
	if v.SigninMode != "normal" && v.SigninMode != "choose" {
		return errors.New("签到模式无效")
	}
	if !oneOf(v.RefundMode, "none", "full", "ratio") || !oneOf(v.RefundInsufficient, "cut_to_zero", "none") {
		return errors.New("退款策略无效")
	}
	var ms []Milestone
	if json.Unmarshal(v.StreakMilestones, &ms) != nil {
		return errors.New("连续签到里程碑格式无效")
	}
	return s.db.WithContext(ctx).Table(settingsTable).Where("id=1").Updates(map[string]any{
		"enabled": v.Enabled, "points_expire_days": v.PointsExpireDays, "show_mall": v.ShowMall, "show_signin": v.ShowSignin, "show_tasks": v.ShowTasks,
		"refund_mode": v.RefundMode, "refund_insufficient": v.RefundInsufficient, "recharge_refund_deduct": v.RechargeRefundDeduct,
		"signin_mode": v.SigninMode, "signin_stable_points": v.SigninStablePoints, "signin_lucky_min": v.SigninLuckyMin, "signin_lucky_max": v.SigninLuckyMax,
		"streak_reward_enabled": v.StreakRewardEnabled, "streak_reset_on_break": v.StreakResetOnBreak, "streak_max_mode": v.StreakMaxMode, "streak_milestones": v.StreakMilestones,
	}).Error
}

func oneOf(v string, values ...string) bool {
	for _, x := range values {
		if v == x {
			return true
		}
	}
	return false
}
func page(v, size int) (int, int) {
	if v < 1 {
		v = 1
	}
	if size < 1 || size > 100 {
		size = 20
	}
	return v, size
}
func periodKey(kind string, now time.Time) string {
	switch kind {
	case "daily":
		return now.Format("20060102")
	case "weekly":
		y, w := now.ISOWeek()
		return fmt.Sprintf("%04dW%02d", y, w)
	case "monthly":
		return now.Format("200601")
	default:
		return "once"
	}
}
func frequencyKey(kind string, now time.Time) string {
	switch kind {
	case "day":
		return now.Format("20060102")
	case "week":
		y, w := now.ISOWeek()
		return fmt.Sprintf("%04dW%02d", y, w)
	case "month":
		return now.Format("200601")
	default:
		return "once"
	}
}
func frequencyTTL(kind string) time.Duration {
	switch kind {
	case "day":
		return 48 * time.Hour
	case "week":
		return 15 * 24 * time.Hour
	case "month":
		return 63 * 24 * time.Hour
	default:
		return 3650 * 24 * time.Hour
	}
}

func (s *Service) allowRule(ctx context.Context, rule *Rule, userID int64) bool {
	if rule.LimitType == "none" || rule.LimitValue <= 0 || s.redis == nil {
		return true
	}
	key := fmt.Sprintf("points:limit:%d:%d:%s", rule.ID, userID, frequencyKey(rule.LimitType, s.now()))
	n, e := s.redis.Incr(ctx, key).Result()
	if e != nil {
		return false
	}
	if n == 1 {
		_ = s.redis.Expire(ctx, key, frequencyTTL(rule.LimitType)).Err()
	}
	return n <= int64(rule.LimitValue)
}

type ladderBand struct {
	Min    int64 `json:"min"`
	Max    int64 `json:"max"`
	Points int64 `json:"points"`
}

func (s *Service) calculate(rule *Rule, amountCents int64) int64 {
	switch rule.Type {
	case "fixed":
		return rule.PointsFixed
	case "random":
		if rule.PointsMax < rule.PointsMin {
			return 0
		}
		return rule.PointsMin + s.random.Int63n(rule.PointsMax-rule.PointsMin+1)
	case "ratio":
		return int64(math.Floor(float64(amountCents) * rule.Ratio / 10000))
	case "ladder":
		var rows []ladderBand
		_ = json.Unmarshal(rule.LadderConfig, &rows)
		for _, b := range rows {
			if amountCents >= b.Min && (b.Max <= 0 || amountCents <= b.Max) {
				return b.Points
			}
		}
	}
	return 0
}

func (s *Service) GrantByEvent(ctx context.Context, event string, userID int64, meta EventMeta) (int64, error) {
	enabled, e := s.Enabled(ctx)
	if e != nil || !enabled {
		return 0, e
	}
	var rules []Rule
	if e = s.db.WithContext(ctx).Table(rulesTable).Where("event=? AND enabled=1", event).Find(&rules).Error; e != nil {
		return 0, e
	}
	var total int64
	for i := range rules {
		r := &rules[i]
		if !s.allowRule(ctx, r, userID) {
			continue
		}
		points := s.calculate(r, meta.AmountCents)
		if points <= 0 {
			continue
		}
		ref := meta.RefID
		if ref == "" {
			ref = fmt.Sprintf("%s-%d", event, s.now().UnixNano())
		}
		remark := meta.Remark
		if remark == "" {
			remark = r.Name
		}
		e = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			return s.creditTx(ctx, tx, userID, points, "rule:"+r.Code, event, ref, remark)
		})
		if e == nil {
			total += points
		} else if !isDuplicate(e) {
			return total, e
		}
	}
	if e = s.progressTasks(ctx, event, userID); e != nil {
		return total, e
	}
	return total, nil
}

func (s *Service) ensureAccountTx(ctx context.Context, tx *gorm.DB, userID int64) error {
	return tx.WithContext(ctx).Exec("INSERT IGNORE INTO system.points_accounts (user_id,status) VALUES (?,1)", userID).Error
}
func (s *Service) creditTx(ctx context.Context, tx *gorm.DB, userID, points int64, source, event, ref, remark string) error {
	if points <= 0 {
		return errors.New("积分必须大于0")
	}
	if e := s.ensureAccountTx(ctx, tx, userID); e != nil {
		return e
	}
	var a Account
	if e := tx.WithContext(ctx).Table(accountsTable).Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id=?", userID).Take(&a).Error; e != nil {
		return e
	}
	if !a.Status {
		return errors.New("积分账户已停用")
	}
	var n int64
	if e := tx.WithContext(ctx).Table(recordsTable).Where("user_id=? AND event=? AND ref_id=? AND source=?", userID, event, ref, source).Count(&n).Error; e != nil {
		return e
	}
	if n > 0 {
		return nil
	}
	settings, e := s.Settings(ctx)
	if e != nil {
		return e
	}
	var expire *time.Time
	if settings.PointsExpireDays > 0 {
		v := s.now().AddDate(0, 0, settings.PointsExpireDays)
		expire = &v
	}
	a.Balance += points
	a.TotalEarned += points
	if e = tx.WithContext(ctx).Table(accountsTable).Where("user_id=?", userID).Updates(map[string]any{"balance": a.Balance, "total_earned": a.TotalEarned}).Error; e != nil {
		return e
	}
	return tx.WithContext(ctx).Table(recordsTable).Create(&Record{UserID: userID, Type: "gain", Source: source, Event: event, Change: points, BalanceAfter: a.Balance, ExpireAt: expire, RefID: ref, Remark: remark}).Error
}
func (s *Service) debitTx(ctx context.Context, tx *gorm.DB, userID, points int64, source, event, ref, remark string, allowPartial bool) (int64, error) {
	if e := s.ensureAccountTx(ctx, tx, userID); e != nil {
		return 0, e
	}
	var a Account
	if e := tx.WithContext(ctx).Table(accountsTable).Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id=?", userID).Take(&a).Error; e != nil {
		return 0, e
	}
	if !a.Status {
		return 0, errors.New("积分账户已停用")
	}
	deduct := points
	if a.Balance < deduct {
		if !allowPartial {
			return 0, errors.New("可用积分不足")
		}
		deduct = a.Balance
	}
	if deduct <= 0 {
		return 0, nil
	}
	var n int64
	if e := tx.WithContext(ctx).Table(recordsTable).Where("user_id=? AND event=? AND ref_id=? AND source=?", userID, event, ref, source).Count(&n).Error; e != nil {
		return 0, e
	}
	if n > 0 {
		return 0, nil
	}
	a.Balance -= deduct
	a.TotalUsed += deduct
	if e := tx.WithContext(ctx).Table(accountsTable).Where("user_id=?", userID).Updates(map[string]any{"balance": a.Balance, "total_used": a.TotalUsed}).Error; e != nil {
		return 0, e
	}
	e := tx.WithContext(ctx).Table(recordsTable).Create(&Record{UserID: userID, Type: "deduct", Source: source, Event: event, Change: -deduct, BalanceAfter: a.Balance, RefID: ref, Remark: remark}).Error
	return deduct, e
}

func (s *Service) Account(ctx context.Context, userID int64) (*Account, error) {
	_ = s.db.WithContext(ctx).Exec("INSERT IGNORE INTO system.points_accounts (user_id,status) VALUES (?,1)", userID).Error
	var v Account
	e := s.db.WithContext(ctx).Table(accountsTable).Where("user_id=?", userID).Take(&v).Error
	return &v, e
}
func (s *Service) Records(ctx context.Context, userID int64, typ, source, keyword, from, to string, p, z int) ([]Record, int64, error) {
	p, z = page(p, z)
	q := s.db.WithContext(ctx).Table(recordsTable).Where("user_id=?", userID)
	if typ != "" {
		q = q.Where("type=?", typ)
	}
	if source != "" {
		q = q.Where("source=?", source)
	}
	if keyword != "" {
		q = q.Where("remark LIKE ?", "%"+keyword+"%")
	}
	if from != "" {
		q = q.Where("created_at>=?", from)
	}
	if to != "" {
		q = q.Where("created_at<=?", to+" 23:59:59")
	}
	var total int64
	var rows []Record
	e := q.Count(&total).Error
	if e == nil {
		e = q.Order("created_at DESC").Offset((p - 1) * z).Limit(z).Find(&rows).Error
	}
	return rows, total, e
}

type SigninCalendar struct {
	Account       *Account   `json:"account"`
	CurrentStreak int        `json:"currentStreak"`
	MonthCount    int64      `json:"monthCount"`
	TotalCount    int64      `json:"totalCount"`
	SignedDates   []string   `json:"signedDates"`
	Entries       []Signin   `json:"entries"`
	Settings      *Settings  `json:"settings"`
	NextMilestone *Milestone `json:"nextMilestone,omitempty"`
}

func (s *Service) Signin(ctx context.Context, userID int64, mode string) (*Signin, error) {
	settings, e := s.Settings(ctx)
	if e != nil {
		return nil, e
	}
	if !settings.ShowSignin {
		return nil, ErrNotFound
	}
	if settings.SigninMode == "normal" {
		mode = "normal"
	} else if mode != "stable" && mode != "lucky" {
		return nil, errors.New("请选择稳定签到或幸运签到")
	}
	lockKey := fmt.Sprintf("points:signin:%d:%s", userID, s.now().Format("20060102"))
	if s.redis != nil {
		ok, err := s.redis.SetNX(ctx, lockKey, "1", 10*time.Second).Result()
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, errors.New("签到正在处理中")
		}
		defer s.redis.Del(ctx, lockKey)
	}
	var created Signin
	e = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		today, _ := time.ParseInLocation("2006-01-02", s.now().Format("2006-01-02"), s.now().Location())
		var existing int64
		if e := tx.Table(signinsTable).Where("user_id=? AND signin_date=?", userID, today).Count(&existing).Error; e != nil {
			return e
		}
		if existing > 0 {
			return errors.New("今天已经签到")
		}
		var last Signin
		lastErr := tx.Table(signinsTable).Where("user_id=?", userID).Order("signin_date DESC").Take(&last).Error
		streak := 1
		if lastErr == nil {
			yesterday := today.AddDate(0, 0, -1)
			if last.SigninDate.Format("2006-01-02") == yesterday.Format("2006-01-02") || !settings.StreakResetOnBreak {
				streak = last.Streak + 1
			}
		}
		var milestones []Milestone
		_ = json.Unmarshal(settings.StreakMilestones, &milestones)
		max := 0
		for _, m := range milestones {
			if m.Days > max {
				max = m.Days
			}
		}
		if max > 0 && streak > max && settings.StreakMaxMode == "reset" {
			streak = 1
		}
		points := settings.SigninStablePoints
		if mode == "lucky" {
			points = settings.SigninLuckyMin + s.random.Int63n(settings.SigninLuckyMax-settings.SigninLuckyMin+1)
		}
		bonus := int64(0)
		if settings.StreakRewardEnabled {
			for _, m := range milestones {
				if m.Days == streak {
					bonus += m.Points
				}
			}
		}
		created = Signin{UserID: userID, SigninDate: today, Mode: mode, Points: points + bonus, Streak: streak}
		if e := tx.Table(signinsTable).Create(&created).Error; e != nil {
			return e
		}
		return s.creditTx(ctx, tx, userID, points+bonus, "signin", "signin", today.Format("2006-01-02"), fmt.Sprintf("每日签到（连续 %d 天）", streak))
	})
	if e != nil {
		return nil, e
	}
	_ = s.progressTasks(ctx, EventSignin, userID)
	return &created, nil
}
func (s *Service) SigninCalendar(ctx context.Context, userID int64, month string) (*SigninCalendar, error) {
	if month == "" {
		month = s.now().Format("2006-01")
	}
	start, e := time.ParseInLocation("2006-01", month, s.now().Location())
	if e != nil {
		return nil, errors.New("月份格式应为 YYYY-MM")
	}
	end := start.AddDate(0, 1, 0)
	var rows []Signin
	var total int64
	if e = s.db.WithContext(ctx).Table(signinsTable).Where("user_id=? AND signin_date>=? AND signin_date<?", userID, start, end).Order("signin_date").Find(&rows).Error; e != nil {
		return nil, e
	}
	if e = s.db.WithContext(ctx).Table(signinsTable).Where("user_id=?", userID).Count(&total).Error; e != nil {
		return nil, e
	}
	a, e := s.Account(ctx, userID)
	if e != nil {
		return nil, e
	}
	settings, e := s.Settings(ctx)
	if e != nil {
		return nil, e
	}
	streak := 0
	var latest Signin
	if s.db.WithContext(ctx).Table(signinsTable).Where("user_id=?", userID).Order("signin_date DESC").Take(&latest).Error == nil {
		streak = latest.Streak
	}
	dates := make([]string, 0, len(rows))
	for _, r := range rows {
		dates = append(dates, r.SigninDate.Format("2006-01-02"))
	}
	var ms []Milestone
	_ = json.Unmarshal(settings.StreakMilestones, &ms)
	var next *Milestone
	for i := range ms {
		if ms[i].Days > streak && (next == nil || ms[i].Days < next.Days) {
			v := ms[i]
			next = &v
		}
	}
	return &SigninCalendar{Account: a, CurrentStreak: streak, MonthCount: int64(len(rows)), TotalCount: total, SignedDates: dates, Entries: rows, Settings: settings, NextMilestone: next}, nil
}

func (s *Service) progressTasks(ctx context.Context, event string, userID int64) error {
	var tasks []Task
	if e := s.db.WithContext(ctx).Table(tasksTable).Where("target_event=? AND enabled=1", event).Find(&tasks).Error; e != nil {
		return e
	}
	now := s.now()
	for _, t := range tasks {
		key := periodKey(t.TaskType, now)
		e := s.db.WithContext(ctx).Exec("INSERT INTO system.points_task_progress (user_id,task_id,period_key,progress) VALUES (?,?,?,1) ON DUPLICATE KEY UPDATE progress=LEAST(progress+1,?)", userID, t.ID, key, t.TargetValue).Error
		if e != nil {
			return e
		}
	}
	return nil
}
func (s *Service) Tasks(ctx context.Context, userID int64, category string) ([]Task, error) {
	q := s.db.WithContext(ctx).Table(tasksTable).Where("enabled=1")
	if category != "" && category != "all" {
		q = q.Where("category=?", category)
	}
	var rows []Task
	if e := q.Order("sort DESC,id").Find(&rows).Error; e != nil {
		return nil, e
	}
	for i := range rows {
		key := periodKey(rows[i].TaskType, s.now())
		var p struct {
			Progress  int64
			ClaimedAt *time.Time
		}
		_ = s.db.WithContext(ctx).Table(progressTable).Select("progress,claimed_at").Where("user_id=? AND task_id=? AND period_key=?", userID, rows[i].ID, key).Take(&p).Error
		rows[i].Progress = p.Progress
		if p.ClaimedAt != nil {
			rows[i].Status = "claimed"
		} else if p.Progress >= rows[i].TargetValue {
			rows[i].Status = "claimable"
		} else {
			rows[i].Status = "progress"
		}
	}
	return rows, nil
}
func (s *Service) ClaimTask(ctx context.Context, userID, taskID int64) (*Account, error) {
	var task Task
	if e := s.db.WithContext(ctx).Table(tasksTable).Where("id=? AND enabled=1", taskID).Take(&task).Error; e != nil {
		return nil, ErrNotFound
	}
	key := periodKey(task.TaskType, s.now())
	e := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var p struct {
			Progress  int64
			ClaimedAt *time.Time
		}
		if e := tx.Table(progressTable).Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id=? AND task_id=? AND period_key=?", userID, taskID, key).Take(&p).Error; e != nil {
			return errors.New("任务尚未完成")
		}
		if p.ClaimedAt != nil {
			return errors.New("奖励已经领取")
		}
		if p.Progress < task.TargetValue {
			return errors.New("任务尚未完成")
		}
		now := s.now()
		if e := tx.Table(progressTable).Where("user_id=? AND task_id=? AND period_key=?", userID, taskID, key).Update("claimed_at", now).Error; e != nil {
			return e
		}
		return s.creditTx(ctx, tx, userID, task.Points, "task", "task", fmt.Sprintf("%d:%s", taskID, key), "任务奖励："+task.Name)
	})
	if e != nil {
		return nil, e
	}
	return s.Account(ctx, userID)
}

func (s *Service) Goods(ctx context.Context, categoryID int64, p, z int) ([]Goods, int64, error) {
	p, z = page(p, z)
	q := s.db.WithContext(ctx).Table(goodsTable).Where("status=1")
	if categoryID > 0 {
		q = q.Where("category_id=?", categoryID)
	}
	var rows []Goods
	var total int64
	e := q.Count(&total).Error
	if e == nil {
		e = q.Order("is_hot DESC,sort DESC,id DESC").Offset((p - 1) * z).Limit(z).Find(&rows).Error
	}
	return rows, total, e
}
func (s *Service) GoodsOne(ctx context.Context, id int64, admin bool) (*Goods, error) {
	var v Goods
	q := s.db.WithContext(ctx).Table(goodsTable).Where("id=?", id)
	if !admin {
		q = q.Where("status=1")
	}
	if e := q.Take(&v).Error; e != nil {
		return nil, ErrNotFound
	}
	return &v, nil
}
func jsonObject(raw json.RawMessage) map[string]any {
	var v map[string]any
	if json.Unmarshal(raw, &v) != nil {
		v = map[string]any{}
	}
	return v
}
func stringValue(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatInt(int64(x), 10)
	default:
		return ""
	}
}
func randomCode() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return strings.ToUpper(hex.EncodeToString(b))
}
func (s *Service) Exchange(ctx context.Context, userID, goodsID int64, quantity int, recipient json.RawMessage) (*Order, error) {
	if quantity < 1 || quantity > 100 {
		return nil, errors.New("兑换数量无效")
	}
	var out Order
	e := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var goods Goods
		if e := tx.Table(goodsTable).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND status=1", goodsID).Take(&goods).Error; e != nil {
			return ErrNotFound
		}
		if goods.Stock >= 0 && goods.Stock-goods.Sold < quantity {
			return errors.New("商品库存不足")
		}
		if goods.Type == "physical" {
			var r map[string]any
			if json.Unmarshal(recipient, &r) != nil || strings.TrimSpace(stringValue(r["name"])) == "" || strings.TrimSpace(stringValue(r["phone"])) == "" || strings.TrimSpace(stringValue(r["address"])) == "" {
				return errors.New("实物商品需要填写完整收货信息")
			}
		}
		total := goods.PointsCost * int64(quantity)
		out = Order{ID: snowflake.GenID(), OrderNo: "PM" + s.now().Format("20060102150405") + fmt.Sprintf("%06d", snowflake.GenID()%1000000), UserID: userID, GoodsID: goods.ID, GoodsName: goods.Name, GoodsType: goods.Type, PointsCost: total, Quantity: quantity, Status: "completed", Recipient: recipient, Tracking: json.RawMessage(`{}`), ConfigSnapshot: goods.Config}
		cfg := jsonObject(goods.Config)
		if goods.Type == "physical" {
			out.Status = "pending"
		}
		if goods.Type == "system" {
			out.Status = "processing"
			if e := s.ensureAccountTx(ctx, tx, userID); e != nil {
				return e
			}
			var a Account
			if e := tx.Table(accountsTable).Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id=?", userID).Take(&a).Error; e != nil {
				return e
			}
			if a.Balance < total {
				return errors.New("可用积分不足")
			}
			a.Balance -= total
			a.Frozen += total
			if e := tx.Table(accountsTable).Where("user_id=?", userID).Updates(map[string]any{"balance": a.Balance, "frozen": a.Frozen}).Error; e != nil {
				return e
			}
		} else {
			if _, e := s.debitTx(ctx, tx, userID, total, "exchange", "exchange", out.OrderNo, "积分商城兑换："+goods.Name, false); e != nil {
				return e
			}
		}
		if goods.Type == "virtual" {
			out.DeliveryContent = stringValue(cfg["content"])
		}
		if goods.Type == "card" {
			if quantity != 1 {
				return errors.New("卡密商品每次限兑 1 件")
			}
			var card Card
			if e := tx.Table(cardsTable).Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).Where("goods_id=? AND used=0", goods.ID).Order("id").Take(&card).Error; e != nil {
				return errors.New("卡密库存不足")
			}
			now := s.now()
			if e := tx.Table(cardsTable).Where("id=? AND used=0", card.ID).Updates(map[string]any{"used": 1, "used_by": userID, "used_at": now}).Error; e != nil {
				return e
			}
			out.DeliveryContent = card.CardValue
			if e := tx.Table(batchesTable).Where("id=?", card.BatchID).Update("used", gorm.Expr("used+1")).Error; e != nil {
				return e
			}
		}
		if goods.Type == "balance" {
			amount, _ := strconv.ParseInt(stringValue(cfg["amountCents"]), 10, 64)
			if amount <= 0 {
				return errors.New("余额商品配置无效")
			}
			if amount > int64(balance.MaxMoney)/int64(quantity) {
				return errors.New("余额商品金额超限")
			}
			if e := tx.Table(ordersTable).Create(&out).Error; e != nil {
				return e
			}
			if _, e := balance.New(tx).Change(ctx, balance.ChangeRequest{CustomerID: userID, Type: "points_credit", Amount: balance.Money(amount * int64(quantity)).String(), RefType: "points_order", RefID: out.ID, Channel: "points", RequestID: "POINTS:" + out.OrderNo, Notes: "积分商城兑换余额"}); e != nil {
				return e
			}
			out.DeliveryContent = "余额已到账：¥" + balance.Money(amount*int64(quantity)).String()
		}
		if goods.Type == "coupon" {
			couponID, e := strconv.ParseInt(stringValue(cfg["couponId"]), 10, 64)
			if e != nil || couponID <= 0 {
				return errors.New("代金券商品配置无效")
			}
			var coupon struct {
				ValidDays int
				Status    int
			}
			if e = tx.Table("marketing.coupons").Select("valid_days,status").Where("id=?", couponID).Take(&coupon).Error; e != nil || coupon.Status != 1 {
				return errors.New("代金券不可发放")
			}
			for i := 0; i < quantity; i++ {
				now := s.now()
				expire := now.AddDate(0, 0, coupon.ValidDays)
				if coupon.ValidDays == 0 {
					expire = now.AddDate(10, 0, 0)
				}
				if e = tx.Exec("INSERT INTO marketing.user_coupons (id,user_id,coupon_id,code,source,status,expire_at,created_at,updated_at) VALUES (?,?,?,?,2,0,?,?,?)", snowflake.GenID(), userID, couponID, "PM-"+randomCode(), expire, now, now).Error; e != nil {
					return e
				}
			}
			if e = tx.Table("marketing.coupons").Where("id=?", couponID).Update("claimed_count", gorm.Expr("claimed_count+?", quantity)).Error; e != nil {
				return e
			}
			out.DeliveryContent = "代金券已发放至账户"
		}
		if e := tx.Table(goodsTable).Where("id=?", goods.ID).Update("sold", gorm.Expr("sold+?", quantity)).Error; e != nil {
			return e
		}
		if goods.Type == "balance" {
			return tx.Table(ordersTable).Where("id=?", out.ID).Update("delivery_content", out.DeliveryContent).Error
		}
		return tx.Table(ordersTable).Create(&out).Error
	})
	if e != nil {
		return nil, e
	}
	return &out, nil
}

func (s *Service) Orders(ctx context.Context, userID int64, admin bool, status string, p, z int) ([]Order, int64, error) {
	p, z = page(p, z)
	q := s.db.WithContext(ctx).Table(ordersTable)
	if !admin {
		q = q.Where("user_id=?", userID)
	}
	if status != "" && status != "all" {
		q = q.Where("status=?", status)
	}
	var rows []Order
	var total int64
	e := q.Count(&total).Error
	if e == nil {
		e = q.Order("created_at DESC").Offset((p - 1) * z).Limit(z).Find(&rows).Error
	}
	if !admin {
		for i := range rows {
			rows[i].Recipient = maskRecipient(rows[i].Recipient)
		}
	}
	return rows, total, e
}
func maskRecipient(raw json.RawMessage) json.RawMessage {
	var v map[string]any
	if json.Unmarshal(raw, &v) != nil {
		return raw
	}
	if p := stringValue(v["phone"]); len(p) >= 7 {
		v["phone"] = p[:3] + "****" + p[len(p)-4:]
	}
	b, _ := json.Marshal(v)
	return b
}
func (s *Service) Order(ctx context.Context, userID, id int64, admin bool) (*Order, error) {
	var v Order
	if e := s.db.WithContext(ctx).Table(ordersTable).Where("id=?", id).Take(&v).Error; e != nil {
		return nil, ErrNotFound
	}
	if !admin {
		if v.UserID != userID {
			return nil, ErrForbidden
		}
		v.Recipient = maskRecipient(v.Recipient)
	}
	return &v, nil
}
func (s *Service) Deliver(ctx context.Context, id int64, tracking json.RawMessage) error {
	return s.db.WithContext(ctx).Table(ordersTable).Where("id=? AND goods_type='physical' AND status IN ('pending','processing')", id).Updates(map[string]any{"tracking": tracking, "status": "processing"}).Error
}
func (s *Service) Confirm(ctx context.Context, userID, id int64) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var order Order
		if e := tx.Table(ordersTable).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", id).Take(&order).Error; e != nil {
			return ErrNotFound
		}
		if order.UserID != userID {
			return ErrForbidden
		}
		if order.GoodsType != "physical" || order.Status != "processing" {
			return errors.New("当前订单不能确认收货")
		}
		return tx.Table(ordersTable).Where("id=?", id).Update("status", "completed").Error
	})
}
func (s *Service) SetOrderStatus(ctx context.Context, id int64, status string) error {
	if !oneOf(status, "processing", "completed", "cancelled") {
		return errors.New("订单状态无效")
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var o Order
		if e := tx.Table(ordersTable).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", id).Take(&o).Error; e != nil {
			return ErrNotFound
		}
		if o.Status == status {
			return nil
		}
		if o.GoodsType == "system" && o.Status == "processing" {
			var a Account
			if e := tx.Table(accountsTable).Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id=?", o.UserID).Take(&a).Error; e != nil {
				return e
			}
			if a.Frozen < o.PointsCost {
				return errors.New("冻结积分数据异常")
			}
			a.Frozen -= o.PointsCost
			if status == "cancelled" {
				a.Balance += o.PointsCost
			} else if status == "completed" {
				a.TotalUsed += o.PointsCost
				if e := tx.Table(recordsTable).Create(&Record{UserID: o.UserID, Type: "deduct", Source: "exchange", Event: "exchange", Change: -o.PointsCost, BalanceAfter: a.Balance, RefID: o.OrderNo, Remark: "系统商品开通成功：" + o.GoodsName}).Error; e != nil {
					return e
				}
			}
			if e := tx.Table(accountsTable).Where("user_id=?", o.UserID).Updates(map[string]any{"balance": a.Balance, "frozen": a.Frozen, "total_used": a.TotalUsed}).Error; e != nil {
				return e
			}
		}
		return tx.Table(ordersTable).Where("id=?", id).Update("status", status).Error
	})
}

func (s *Service) ImportCards(ctx context.Context, goodsID int64, values []string) (*CardBatch, error) {
	if len(values) == 0 {
		return nil, errors.New("请输入卡密")
	}
	g, e := s.GoodsOne(ctx, goodsID, true)
	if e != nil || g.Type != "card" {
		return nil, errors.New("卡密商品不存在")
	}
	batch := &CardBatch{GoodsID: goodsID, Total: len(values)}
	e = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := tx.Table(batchesTable).Create(batch).Error; e != nil {
			return e
		}
		seen := map[string]bool{}
		for _, v := range values {
			v = strings.TrimSpace(v)
			if v == "" || seen[v] {
				continue
			}
			seen[v] = true
			if e := tx.Table(cardsTable).Create(&Card{BatchID: batch.ID, GoodsID: goodsID, CardValue: v}).Error; e != nil {
				return e
			}
		}
		return tx.Table(goodsTable).Where("id=?", goodsID).Update("stock", gorm.Expr("CASE WHEN stock < 0 THEN ? ELSE stock + ? END", len(seen), len(seen))).Error
	})
	return batch, e
}

func (s *Service) RefundDeduct(ctx context.Context, userID int64, event, refID string, ratio float64) (int64, error) {
	settings, e := s.Settings(ctx)
	if e != nil {
		return 0, e
	}
	if settings.RefundMode == "none" || (event == EventRecharge && !settings.RechargeRefundDeduct) {
		return 0, nil
	}
	var earned int64
	if e = s.db.WithContext(ctx).Table(recordsTable).Where("user_id=? AND ref_id=? AND type='gain'", userID, refID).Select("COALESCE(SUM(change_value),0)").Scan(&earned).Error; e != nil {
		return 0, e
	}
	deduct := earned
	if settings.RefundMode == "ratio" {
		deduct = int64(math.Floor(float64(earned) * ratio))
	}
	var done int64
	e = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var er error
		done, er = s.debitTx(ctx, tx, userID, deduct, "refund", "refund", refID, "业务退款，扣回赠送积分", settings.RefundInsufficient == "cut_to_zero")
		return er
	})
	if e == nil && done > 0 {
		_ = s.EvaluateRisk(ctx, userID)
	}
	return done, e
}
func (s *Service) ExpireDue(ctx context.Context) (int, error) {
	var rows []Record
	e := s.db.WithContext(ctx).Table(recordsTable+" gain").Select("gain.*").Joins("LEFT JOIN system.points_records expired ON expired.event='expire' AND expired.ref_id=CAST(gain.id AS CHAR) AND expired.source='expiry'").Where("gain.type='gain' AND gain.expire_at IS NOT NULL AND gain.expire_at<=? AND expired.id IS NULL", s.now()).Limit(1000).Find(&rows).Error
	if e != nil {
		return 0, e
	}
	n := 0
	for _, r := range rows {
		e = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			_, x := s.debitTx(ctx, tx, r.UserID, r.Change, "expiry", "expire", strconv.FormatInt(r.ID, 10), "积分到期清零", true)
			return x
		})
		if e == nil {
			n++
		}
	}
	return n, nil
}
func (s *Service) StartExpiryScanner(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				distsync.Task(ctx, "points_expiry", time.Minute, func(ctx context.Context) error { _, err := s.ExpireDue(ctx); return err })
			}
		}
	}()
}

func (s *Service) ListRules(ctx context.Context, enabledOnly bool) ([]Rule, error) {
	var rows []Rule
	q := s.db.WithContext(ctx).Table(rulesTable)
	if enabledOnly {
		q = q.Where("enabled=1")
	}
	e := q.Order("created_at DESC").Find(&rows).Error
	return rows, e
}
func (s *Service) SaveRule(ctx context.Context, v *Rule) error {
	if strings.TrimSpace(v.Name) == "" || strings.TrimSpace(v.Code) == "" || !oneOf(v.Type, "fixed", "random", "ratio", "ladder") || !oneOf(v.LimitType, "none", "once", "day", "week", "month") {
		return errors.New("规则参数无效")
	}
	if v.ID == 0 {
		return s.db.WithContext(ctx).Table(rulesTable).Create(v).Error
	}
	return s.db.WithContext(ctx).Table(rulesTable).Where("id=?", v.ID).Updates(v).Error
}
func (s *Service) DeleteRule(ctx context.Context, id int64) error {
	return s.db.WithContext(ctx).Table(rulesTable).Where("id=?", id).Delete(&Rule{}).Error
}
func (s *Service) ListTasksAdmin(ctx context.Context) ([]Task, error) {
	var rows []Task
	e := s.db.WithContext(ctx).Table(tasksTable).Order("sort DESC,id DESC").Find(&rows).Error
	return rows, e
}
func (s *Service) SaveTask(ctx context.Context, v *Task) error {
	if strings.TrimSpace(v.Name) == "" || strings.TrimSpace(v.Code) == "" || v.TargetValue < 1 || v.Points < 1 || !oneOf(v.TaskType, "once", "daily", "weekly", "monthly") || !oneOf(v.Category, "newbie", "daily", "growth") {
		return errors.New("任务参数无效")
	}
	if v.ID == 0 {
		return s.db.WithContext(ctx).Table(tasksTable).Create(v).Error
	}
	return s.db.WithContext(ctx).Table(tasksTable).Where("id=?", v.ID).Updates(v).Error
}
func (s *Service) DeleteTask(ctx context.Context, id int64) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := tx.Exec("DELETE FROM system.points_task_progress WHERE task_id=?", id).Error; e != nil {
			return e
		}
		return tx.Table(tasksTable).Where("id=?", id).Delete(&Task{}).Error
	})
}
func (s *Service) ListGoodsAdmin(ctx context.Context) ([]Goods, error) {
	var rows []Goods
	e := s.db.WithContext(ctx).Table(goodsTable).Order("sort DESC,id DESC").Find(&rows).Error
	return rows, e
}
func (s *Service) SaveGoods(ctx context.Context, v *Goods) error {
	if strings.TrimSpace(v.Name) == "" || v.PointsCost < 1 || !oneOf(v.Type, "virtual", "physical", "coupon", "balance", "system", "card") {
		return errors.New("商品参数无效")
	}
	if len(v.Config) == 0 {
		v.Config = json.RawMessage(`{}`)
	}
	if v.ID == 0 {
		return s.db.WithContext(ctx).Table(goodsTable).Create(v).Error
	}
	return s.db.WithContext(ctx).Table(goodsTable).Where("id=?", v.ID).Updates(v).Error
}
func (s *Service) DeleteGoods(ctx context.Context, id int64) error {
	var n int64
	if e := s.db.WithContext(ctx).Table(ordersTable).Where("goods_id=?", id).Count(&n).Error; e != nil {
		return e
	}
	if n > 0 {
		return errors.New("商品已有兑换记录，不能删除")
	}
	return s.db.WithContext(ctx).Table(goodsTable).Where("id=?", id).Delete(&Goods{}).Error
}
func (s *Service) Categories(ctx context.Context) ([]Category, error) {
	var rows []Category
	e := s.db.WithContext(ctx).Table(categoriesTable).Order("sort DESC,id").Find(&rows).Error
	return rows, e
}
func (s *Service) SaveCategory(ctx context.Context, v *Category) error {
	if strings.TrimSpace(v.Name) == "" {
		return errors.New("分类名称不能为空")
	}
	if v.ID == 0 {
		return s.db.WithContext(ctx).Table(categoriesTable).Create(v).Error
	}
	if v.ParentID == v.ID {
		return errors.New("分类不能作为自己的上级")
	}
	return s.db.WithContext(ctx).Table(categoriesTable).Where("id=?", v.ID).Updates(v).Error
}
func (s *Service) DeleteCategory(ctx context.Context, id int64) error {
	var n int64
	_ = s.db.WithContext(ctx).Table(goodsTable).Where("category_id=?", id).Count(&n).Error
	if n > 0 {
		return errors.New("分类下有商品，不能删除")
	}
	_ = s.db.WithContext(ctx).Table(categoriesTable).Where("parent_id=?", id).Count(&n).Error
	if n > 0 {
		return errors.New("分类下有子分类，不能删除")
	}
	return s.db.WithContext(ctx).Table(categoriesTable).Where("id=?", id).Delete(&Category{}).Error
}

func (s *Service) Accounts(ctx context.Context, keyword string, status *bool, p, z int) ([]Account, int64, error) {
	p, z = page(p, z)
	q := s.db.WithContext(ctx).Table(accountsTable + " pa").Select("pa.*,c.username").Joins("LEFT JOIN customer.customers c ON c.id=pa.user_id")
	if keyword != "" {
		q = q.Where("c.username LIKE ? OR CAST(pa.user_id AS CHAR) LIKE ?", "%"+keyword+"%", "%"+keyword+"%")
	}
	if status != nil {
		q = q.Where("pa.status=?", *status)
	}
	var total int64
	var rows []Account
	e := q.Count(&total).Error
	if e == nil {
		e = q.Order("pa.updated_at DESC").Offset((p - 1) * z).Limit(z).Find(&rows).Error
	}
	for i := range rows {
		var tag RiskTag
		_ = s.db.WithContext(ctx).Table(riskTagsTable+" rt").Select("rt.*").Joins("JOIN system.points_user_tags ut ON ut.tag_id=rt.id").Where("ut.user_id=?", rows[i].UserID).Take(&tag).Error
		if tag.ID > 0 {
			rows[i].RiskTag = &tag
		}
	}
	return rows, total, e
}
func (s *Service) SetAccountStatus(ctx context.Context, userID int64, status bool) error {
	_ = s.db.WithContext(ctx).Exec("INSERT IGNORE INTO system.points_accounts (user_id,status) VALUES (?,?)", userID, status).Error
	return s.db.WithContext(ctx).Table(accountsTable).Where("user_id=?", userID).Update("status", status).Error
}
func (s *Service) AdminGrant(ctx context.Context, userIDs []int64, points int64, remark string) (int, error) {
	if points == 0 || len(userIDs) == 0 {
		return 0, errors.New("请选择用户并填写非零积分")
	}
	n := 0
	for _, id := range userIDs {
		ref := fmt.Sprintf("manual:%d:%d", s.now().UnixNano(), id)
		var e error
		if points > 0 {
			e = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error { return s.creditTx(ctx, tx, id, points, "manual", EventManual, ref, remark) })
		} else {
			e = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
				_, x := s.debitTx(ctx, tx, id, -points, "manual", EventManual, ref, remark, false)
				return x
			})
		}
		if e != nil {
			return n, e
		}
		n++
	}
	return n, nil
}

func (s *Service) RiskTags(ctx context.Context) ([]RiskTag, error) {
	var rows []RiskTag
	e := s.db.WithContext(ctx).Table(riskTagsTable).Order("level,id").Find(&rows).Error
	return rows, e
}
func (s *Service) SaveRiskTag(ctx context.Context, v *RiskTag) error {
	if v.Level < 1 || v.Level > 4 || strings.TrimSpace(v.Name) == "" {
		return errors.New("风控标签参数无效")
	}
	if v.ID == 0 {
		return s.db.WithContext(ctx).Table(riskTagsTable).Create(v).Error
	}
	return s.db.WithContext(ctx).Table(riskTagsTable).Where("id=?", v.ID).Updates(v).Error
}
func (s *Service) DeleteRiskTag(ctx context.Context, id int64) error {
	var n int64
	_ = s.db.WithContext(ctx).Table(userTagsTable).Where("tag_id=?", id).Count(&n).Error
	if n > 0 {
		return errors.New("标签正在使用，不能删除")
	}
	return s.db.WithContext(ctx).Table(riskTagsTable).Where("id=?", id).Delete(&RiskTag{}).Error
}
func (s *Service) RiskRules(ctx context.Context) ([]RiskRule, error) {
	var rows []RiskRule
	e := s.db.WithContext(ctx).Table(riskRulesTable).Order("created_at DESC").Find(&rows).Error
	return rows, e
}

type riskCondition struct {
	Metric     string  `json:"metric"`
	Operator   string  `json:"operator"`
	Value      float64 `json:"value"`
	WindowDays int     `json:"windowDays"`
	Logic      string  `json:"logic"`
}

func compareRisk(actual float64, operator string, expected float64) bool {
	switch operator {
	case ">":
		return actual > expected
	case "<=":
		return actual <= expected
	case "<":
		return actual < expected
	case "=", "==":
		return actual == expected
	default:
		return actual >= expected
	}
}

func (s *Service) riskMetric(ctx context.Context, userID int64, condition riskCondition) (float64, error) {
	days := condition.WindowDays
	if days <= 0 {
		days = 30
	}
	since := s.now().AddDate(0, 0, -days)
	q := s.db.WithContext(ctx).Table(recordsTable).Where("user_id=? AND source='refund' AND created_at>=?", userID, since)
	switch condition.Metric {
	case "refund_amount":
		var amount float64
		e := q.Select("COALESCE(SUM(ABS(change_value)),0)").Scan(&amount).Error
		return amount, e
	case "refund_rate":
		var refunds, gains int64
		if e := q.Count(&refunds).Error; e != nil {
			return 0, e
		}
		if e := s.db.WithContext(ctx).Table(recordsTable).Where("user_id=? AND event IN ? AND type='gain' AND created_at>=?", userID, []string{EventOrder, EventRecharge}, since).Count(&gains).Error; e != nil {
			return 0, e
		}
		if gains == 0 {
			return 0, nil
		}
		return float64(refunds) / float64(gains) * 100, nil
	case "consecutive_refund_days":
		var dates []time.Time
		if e := q.Distinct("DATE(created_at)").Order("DATE(created_at) DESC").Pluck("DATE(created_at)", &dates).Error; e != nil {
			return 0, e
		}
		streak := 0
		for i, date := range dates {
			if i > 0 && dates[i-1].Truncate(24*time.Hour).Sub(date.Truncate(24*time.Hour)) != 24*time.Hour {
				break
			}
			streak++
		}
		return float64(streak), nil
	default: // refund_count
		var count int64
		e := q.Count(&count).Error
		return float64(count), e
	}
}

// EvaluateRisk recalculates the automatic risk tag after a refund. A manually
// locked tag always wins; otherwise the highest-level matching rule is applied.
func (s *Service) EvaluateRisk(ctx context.Context, userID int64) error {
	var current UserTag
	e := s.db.WithContext(ctx).Table(userTagsTable).Where("user_id=?", userID).Take(&current).Error
	if e == nil && current.IsLocked {
		return nil
	}
	if e != nil && !errors.Is(e, gorm.ErrRecordNotFound) {
		return e
	}
	var rules []RiskRule
	if e = s.db.WithContext(ctx).Table(riskRulesTable + " rr").Select("rr.*").Joins("JOIN system.points_risk_tags rt ON rt.id=rr.tag_id").Where("rr.enabled=1").Order("rt.level DESC,rr.id").Find(&rules).Error; e != nil {
		return e
	}
	var matched *RiskRule
	for i := range rules {
		var conditions []riskCondition
		if json.Unmarshal(rules[i].Conditions, &conditions) != nil || len(conditions) == 0 {
			continue
		}
		hit := true
		for j, condition := range conditions {
			actual, metricErr := s.riskMetric(ctx, userID, condition)
			if metricErr != nil {
				return metricErr
			}
			conditionHit := compareRisk(actual, condition.Operator, condition.Value)
			if j == 0 {
				hit = conditionHit
			} else if strings.EqualFold(condition.Logic, "OR") {
				hit = hit || conditionHit
			} else {
				hit = hit && conditionHit
			}
		}
		if hit {
			matched = &rules[i]
			break
		}
	}
	if matched == nil || current.TagID == matched.TagID {
		return nil
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		after := UserTag{UserID: userID, TagID: matched.TagID, Source: "auto"}
		if e := tx.Table(userTagsTable).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "user_id"}}, DoUpdates: clause.AssignmentColumns([]string{"tag_id", "is_locked", "source", "updated_at"})}).Create(&after).Error; e != nil {
			return e
		}
		var before *int64
		if current.TagID > 0 {
			id := current.TagID
			before = &id
		}
		afterID := matched.TagID
		return tx.Table(riskLogsTable).Create(&RiskLog{UserID: userID, TagIDBefore: before, TagIDAfter: &afterID, Source: "auto", Remark: "退款事件命中风控规则：" + matched.Name}).Error
	})
}
func (s *Service) SaveRiskRule(ctx context.Context, v *RiskRule) error {
	if strings.TrimSpace(v.Name) == "" || v.TagID <= 0 || !json.Valid(v.Conditions) {
		return errors.New("风控规则参数无效")
	}
	if v.ID == 0 {
		return s.db.WithContext(ctx).Table(riskRulesTable).Create(v).Error
	}
	return s.db.WithContext(ctx).Table(riskRulesTable).Where("id=?", v.ID).Updates(v).Error
}
func (s *Service) DeleteRiskRule(ctx context.Context, id int64) error {
	return s.db.WithContext(ctx).Table(riskRulesTable).Where("id=?", id).Delete(&RiskRule{}).Error
}
func (s *Service) SetUserTag(ctx context.Context, userID, tagID, operatorID int64, locked bool, remark string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var before UserTag
		_ = tx.Table(userTagsTable).Where("user_id=?", userID).Take(&before).Error
		after := UserTag{UserID: userID, TagID: tagID, IsLocked: locked, Source: "manual"}
		if e := tx.Table(userTagsTable).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "user_id"}}, DoUpdates: clause.AssignmentColumns([]string{"tag_id", "is_locked", "source", "updated_at"})}).Create(&after).Error; e != nil {
			return e
		}
		var b *int64
		if before.TagID > 0 {
			v := before.TagID
			b = &v
		}
		a := tagID
		op := operatorID
		return tx.Table(riskLogsTable).Create(&RiskLog{UserID: userID, TagIDBefore: b, TagIDAfter: &a, Source: "manual", OperatorID: &op, Remark: remark}).Error
	})
}
func (s *Service) RiskLogs(ctx context.Context, p, z int) ([]RiskLog, int64, error) {
	p, z = page(p, z)
	q := s.db.WithContext(ctx).Table(riskLogsTable)
	var rows []RiskLog
	var total int64
	e := q.Count(&total).Error
	if e == nil {
		e = q.Order("created_at DESC").Offset((p - 1) * z).Limit(z).Find(&rows).Error
	}
	return rows, total, e
}
func (s *Service) Stats(ctx context.Context) (map[string]any, error) {
	var earned, used, balance int64
	var accounts int64
	if e := s.db.WithContext(ctx).Table(accountsTable).Select("COALESCE(SUM(total_earned),0),COALESCE(SUM(total_used),0),COALESCE(SUM(balance),0),COUNT(*)").Row().Scan(&earned, &used, &balance, &accounts); e != nil {
		return nil, e
	}
	var sources []struct {
		Source string `json:"source"`
		Points int64  `json:"points"`
	}
	e := s.db.WithContext(ctx).Table(recordsTable).Select("source,COALESCE(SUM(ABS(change_value)),0) points").Group("source").Order("points DESC").Scan(&sources).Error
	return map[string]any{"totalEarned": earned, "totalUsed": used, "balance": balance, "accounts": accounts, "sources": sources}, e
}

func isDuplicate(e error) bool {
	return errors.Is(e, gorm.ErrDuplicatedKey) || strings.Contains(strings.ToLower(e.Error()), "duplicate")
}
