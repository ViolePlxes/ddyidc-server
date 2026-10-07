package points

import (
	"encoding/json"
	"time"

	"ddyidc/internal/shared/snowflake"
	"gorm.io/gorm"
)

const (
	settingsTable   = "system.points_settings"
	rulesTable      = "system.points_rules"
	tasksTable      = "system.points_tasks"
	progressTable   = "system.points_task_progress"
	accountsTable   = "system.points_accounts"
	recordsTable    = "system.points_records"
	signinsTable    = "system.points_signins"
	goodsTable      = "system.points_goods"
	categoriesTable = "system.points_goods_categories"
	batchesTable    = "system.points_card_batches"
	cardsTable      = "system.points_cards"
	ordersTable     = "system.points_orders"
	riskTagsTable   = "system.points_risk_tags"
	riskRulesTable  = "system.points_risk_rules"
	userTagsTable   = "system.points_user_tags"
	riskLogsTable   = "system.points_risk_logs"
)

const (
	EventDouyinFollow    = "douyin_follow"
	EventKuaishouFollow  = "kuaishou_follow"
	EventBilibiliFollow  = "bilibili_follow"
	EventXhsFollow       = "xhs_follow"
	EventRegister        = "register"
	EventLogin           = "login"
	EventSignin          = "signin"
	EventRecharge        = "recharge"
	EventOrder           = "order"
	EventRenew           = "renew"
	EventUpgrade         = "upgrade"
	EventFirstPurchase   = "first_purchase"
	EventGroupBuy        = "group_buy"
	EventSeckill         = "seckill"
	EventTicket          = "ticket"
	EventInvite          = "invite"
	EventProfileComplete = "profile_complete"
	EventCertification   = "certification"
	EventManual          = "manual"
)

type Milestone struct {
	Days   int   `json:"days"`
	Points int64 `json:"points"`
}

type Settings struct {
	DailyRewardLimit      int64           `gorm:"column:daily_reward_limit" json:"dailyRewardLimit"`
	InviteMonthlyLimit    int64           `gorm:"column:invite_monthly_limit" json:"inviteMonthlyLimit"`
	CheckoutDailyLimit    int             `gorm:"column:checkout_daily_limit" json:"checkoutDailyLimit"`
	ID                    int64           `gorm:"column:id;primaryKey" json:"id,string"`
	Enabled               bool            `gorm:"column:enabled" json:"enabled"`
	PointsExpireDays      int             `gorm:"column:points_expire_days" json:"pointsExpireDays"`
	ShowMall              bool            `gorm:"column:show_mall" json:"showMall"`
	ShowSignin            bool            `gorm:"column:show_signin" json:"showSignin"`
	ShowTasks             bool            `gorm:"column:show_tasks" json:"showTasks"`
	RefundMode            string          `gorm:"column:refund_mode" json:"refundMode"`
	RefundInsufficient    string          `gorm:"column:refund_insufficient" json:"refundInsufficient"`
	RechargeRefundDeduct  bool            `gorm:"column:recharge_refund_deduct" json:"rechargeRefundDeduct"`
	SigninMode            string          `gorm:"column:signin_mode" json:"signinMode"`
	SigninProgressive     bool            `gorm:"column:signin_progressive" json:"signinProgressive"`
	CheckoutEnabled       bool            `gorm:"column:checkout_enabled" json:"checkoutEnabled"`
	CheckoutPointsPerYuan int64           `gorm:"column:checkout_points_per_yuan" json:"checkoutPointsPerYuan"`
	CheckoutCouponDays    int             `gorm:"column:checkout_coupon_days" json:"checkoutCouponDays"`
	SigninStablePoints    int64           `gorm:"column:signin_stable_points" json:"signinStablePoints"`
	SigninLuckyMin        int64           `gorm:"column:signin_lucky_min" json:"signinLuckyMin"`
	SigninLuckyMax        int64           `gorm:"column:signin_lucky_max" json:"signinLuckyMax"`
	StreakRewardEnabled   bool            `gorm:"column:streak_reward_enabled" json:"streakRewardEnabled"`
	StreakResetOnBreak    bool            `gorm:"column:streak_reset_on_break" json:"streakResetOnBreak"`
	StreakMaxMode         string          `gorm:"column:streak_max_mode" json:"streakMaxMode"`
	StreakMilestones      json.RawMessage `gorm:"column:streak_milestones;type:json" json:"streakMilestones"`
	CreatedAt             time.Time       `gorm:"column:created_at" json:"createdAt"`
	UpdatedAt             time.Time       `gorm:"column:updated_at" json:"updatedAt"`
}

type Rule struct {
	ID           int64           `gorm:"column:id;primaryKey" json:"id,string"`
	Name         string          `gorm:"column:name" json:"name"`
	Code         string          `gorm:"column:code" json:"code"`
	Event        string          `gorm:"column:event" json:"event"`
	Type         string          `gorm:"column:type" json:"type"`
	PointsFixed  int64           `gorm:"column:points_fixed" json:"pointsFixed"`
	PointsMin    int64           `gorm:"column:points_min" json:"pointsMin"`
	PointsMax    int64           `gorm:"column:points_max" json:"pointsMax"`
	Ratio        float64         `gorm:"column:ratio" json:"ratio"`
	LadderConfig json.RawMessage `gorm:"column:ladder_config;type:json" json:"ladderConfig"`
	LimitType    string          `gorm:"column:limit_type" json:"limitType"`
	LimitValue   int             `gorm:"column:limit_value" json:"limitValue"`
	Enabled      bool            `gorm:"column:enabled" json:"enabled"`
	CreatedAt    time.Time       `gorm:"column:created_at" json:"createdAt"`
	UpdatedAt    time.Time       `gorm:"column:updated_at" json:"updatedAt"`
}

func (v *Rule) BeforeCreate(*gorm.DB) error {
	if v.ID == 0 {
		v.ID = snowflake.GenID()
	}
	return nil
}

type Task struct {
	ID          int64     `gorm:"column:id;primaryKey" json:"id,string"`
	Name        string    `gorm:"column:name" json:"name"`
	Code        string    `gorm:"column:code" json:"code"`
	Description string    `gorm:"column:description" json:"description"`
	TargetEvent string    `gorm:"column:target_event" json:"targetEvent"`
	TaskType    string    `gorm:"column:task_type" json:"taskType"`
	Category    string    `gorm:"column:category" json:"category"`
	Points      int64     `gorm:"column:points" json:"points"`
	TargetValue int64     `gorm:"column:target_value" json:"targetValue"`
	Enabled     bool      `gorm:"column:enabled" json:"enabled"`
	Sort        int       `gorm:"column:sort" json:"sort"`
	CreatedAt   time.Time `gorm:"column:created_at" json:"createdAt"`
	UpdatedAt   time.Time `gorm:"column:updated_at" json:"updatedAt"`
	Progress    int64     `gorm:"-" json:"progress"`
	Status      string    `gorm:"-" json:"claimStatus"`
}

func (v *Task) BeforeCreate(*gorm.DB) error {
	if v.ID == 0 {
		v.ID = snowflake.GenID()
	}
	return nil
}

type Account struct {
	UserID      int64     `gorm:"column:user_id;primaryKey" json:"userId,string"`
	Balance     int64     `gorm:"column:balance" json:"balance"`
	Frozen      int64     `gorm:"column:frozen" json:"frozen"`
	TotalEarned int64     `gorm:"column:total_earned" json:"totalEarned"`
	TotalUsed   int64     `gorm:"column:total_used" json:"totalUsed"`
	Status      bool      `gorm:"column:status" json:"status"`
	UpdatedAt   time.Time `gorm:"column:updated_at" json:"updatedAt"`
	Username    string    `gorm:"-" json:"username,omitempty"`
	RiskTag     *RiskTag  `gorm:"-" json:"riskTag,omitempty"`
}

type Record struct {
	LedgerSequence uint64     `gorm:"column:ledger_seq;->" json:"-"`
	ID             int64      `gorm:"column:id;primaryKey" json:"id,string"`
	UserID         int64      `gorm:"column:user_id" json:"userId,string"`
	Type           string     `gorm:"column:type" json:"type"`
	Source         string     `gorm:"column:source" json:"source"`
	Event          string     `gorm:"column:event" json:"event"`
	Change         int64      `gorm:"column:change_value" json:"change"`
	BalanceAfter   int64      `gorm:"column:balance_after" json:"balanceAfter"`
	ExpireAt       *time.Time `gorm:"column:expire_at" json:"expireAt,omitempty"`
	RefID          string     `gorm:"column:ref_id" json:"refId"`
	Remark         string     `gorm:"column:remark" json:"remark"`
	CreatedAt      time.Time  `gorm:"column:created_at" json:"createdAt"`
}

func (v *Record) BeforeCreate(*gorm.DB) error {
	if v.ID == 0 {
		v.ID = snowflake.GenID()
	}
	return nil
}

type Signin struct {
	ID         int64     `gorm:"column:id;primaryKey" json:"id,string"`
	UserID     int64     `gorm:"column:user_id" json:"userId,string"`
	SigninDate time.Time `gorm:"column:signin_date" json:"signinDate"`
	Mode       string    `gorm:"column:mode" json:"mode"`
	Points     int64     `gorm:"column:points" json:"points"`
	Streak     int       `gorm:"column:streak" json:"streak"`
	CreatedAt  time.Time `gorm:"column:created_at" json:"createdAt"`
}

func (v *Signin) BeforeCreate(*gorm.DB) error {
	if v.ID == 0 {
		v.ID = snowflake.GenID()
	}
	return nil
}

type Category struct {
	ID        int64     `gorm:"column:id;primaryKey" json:"id,string"`
	ParentID  int64     `gorm:"column:parent_id" json:"parentId,string"`
	Name      string    `gorm:"column:name" json:"name"`
	Sort      int       `gorm:"column:sort" json:"sort"`
	CreatedAt time.Time `gorm:"column:created_at" json:"createdAt"`
	UpdatedAt time.Time `gorm:"column:updated_at" json:"updatedAt"`
}

func (v *Category) BeforeCreate(*gorm.DB) error {
	if v.ID == 0 {
		v.ID = snowflake.GenID()
	}
	return nil
}

type Goods struct {
	ID          int64           `gorm:"column:id;primaryKey" json:"id,string"`
	CategoryID  int64           `gorm:"column:category_id" json:"categoryId,string"`
	Name        string          `gorm:"column:name" json:"name"`
	Type        string          `gorm:"column:type" json:"type"`
	Image       string          `gorm:"column:image" json:"image"`
	Description string          `gorm:"column:description" json:"description"`
	PointsCost  int64           `gorm:"column:points_cost" json:"pointsCost"`
	Stock       int             `gorm:"column:stock" json:"stock"`
	Sold        int             `gorm:"column:sold" json:"sold"`
	Status      bool            `gorm:"column:status" json:"status"`
	Config      json.RawMessage `gorm:"column:config;type:json" json:"config"`
	IsHot       bool            `gorm:"column:is_hot" json:"isHot"`
	Sort        int             `gorm:"column:sort" json:"sort"`
	CreatedAt   time.Time       `gorm:"column:created_at" json:"createdAt"`
	UpdatedAt   time.Time       `gorm:"column:updated_at" json:"updatedAt"`
}

func (v *Goods) BeforeCreate(*gorm.DB) error {
	if v.ID == 0 {
		v.ID = snowflake.GenID()
	}
	return nil
}

type CardBatch struct {
	ID        int64     `gorm:"column:id;primaryKey" json:"id,string"`
	GoodsID   int64     `gorm:"column:goods_id" json:"goodsId,string"`
	Total     int       `gorm:"column:total" json:"total"`
	Used      int       `gorm:"column:used" json:"used"`
	CreatedAt time.Time `gorm:"column:created_at" json:"createdAt"`
}

func (v *CardBatch) BeforeCreate(*gorm.DB) error {
	if v.ID == 0 {
		v.ID = snowflake.GenID()
	}
	return nil
}

type Card struct {
	ID        int64      `gorm:"column:id;primaryKey" json:"id,string"`
	BatchID   int64      `gorm:"column:batch_id" json:"batchId,string"`
	GoodsID   int64      `gorm:"column:goods_id" json:"goodsId,string"`
	CardValue string     `gorm:"column:card_value" json:"cardValue,omitempty"`
	Used      bool       `gorm:"column:used" json:"used"`
	UsedBy    *int64     `gorm:"column:used_by" json:"usedBy,string,omitempty"`
	UsedAt    *time.Time `gorm:"column:used_at" json:"usedAt,omitempty"`
}

func (v *Card) BeforeCreate(*gorm.DB) error {
	if v.ID == 0 {
		v.ID = snowflake.GenID()
	}
	return nil
}

type Order struct {
	ID              int64           `gorm:"column:id;primaryKey" json:"id,string"`
	OrderNo         string          `gorm:"column:order_no" json:"orderNo"`
	UserID          int64           `gorm:"column:user_id" json:"userId,string"`
	GoodsID         int64           `gorm:"column:goods_id" json:"goodsId,string"`
	GoodsName       string          `gorm:"column:goods_name" json:"goodsName"`
	GoodsType       string          `gorm:"column:goods_type" json:"goodsType"`
	PointsCost      int64           `gorm:"column:points_cost" json:"pointsCost"`
	Quantity        int             `gorm:"column:quantity" json:"quantity"`
	Status          string          `gorm:"column:status" json:"status"`
	Recipient       json.RawMessage `gorm:"column:recipient;type:json" json:"recipient"`
	Tracking        json.RawMessage `gorm:"column:tracking;type:json" json:"tracking"`
	ConfigSnapshot  json.RawMessage `gorm:"column:config_snapshot;type:json" json:"configSnapshot"`
	DeliveryContent string          `gorm:"column:delivery_content" json:"deliveryContent,omitempty"`
	CreatedAt       time.Time       `gorm:"column:created_at" json:"createdAt"`
	UpdatedAt       time.Time       `gorm:"column:updated_at" json:"updatedAt"`
}

func (v *Order) BeforeCreate(*gorm.DB) error {
	if v.ID == 0 {
		v.ID = snowflake.GenID()
	}
	return nil
}

type RiskTag struct {
	ID              int64     `gorm:"column:id;primaryKey" json:"id,string"`
	Name            string    `gorm:"column:name" json:"name"`
	Level           int       `gorm:"column:level" json:"level"`
	Color           string    `gorm:"column:color" json:"color"`
	Description     string    `gorm:"column:description" json:"description"`
	IsLockedDefault bool      `gorm:"column:is_locked_default" json:"isLockedDefault"`
	CreatedAt       time.Time `gorm:"column:created_at" json:"createdAt"`
	UpdatedAt       time.Time `gorm:"column:updated_at" json:"updatedAt"`
}

func (v *RiskTag) BeforeCreate(*gorm.DB) error {
	if v.ID == 0 {
		v.ID = snowflake.GenID()
	}
	return nil
}

type RiskRule struct {
	ID         int64           `gorm:"column:id;primaryKey" json:"id,string"`
	Name       string          `gorm:"column:name" json:"name"`
	TagID      int64           `gorm:"column:tag_id" json:"tagId,string"`
	Conditions json.RawMessage `gorm:"column:conditions;type:json" json:"conditions"`
	Enabled    bool            `gorm:"column:enabled" json:"enabled"`
	CreatedAt  time.Time       `gorm:"column:created_at" json:"createdAt"`
	UpdatedAt  time.Time       `gorm:"column:updated_at" json:"updatedAt"`
}

func (v *RiskRule) BeforeCreate(*gorm.DB) error {
	if v.ID == 0 {
		v.ID = snowflake.GenID()
	}
	return nil
}

type UserTag struct {
	UserID    int64     `gorm:"column:user_id;primaryKey" json:"userId,string"`
	TagID     int64     `gorm:"column:tag_id" json:"tagId,string"`
	IsLocked  bool      `gorm:"column:is_locked" json:"isLocked"`
	Source    string    `gorm:"column:source" json:"source"`
	UpdatedAt time.Time `gorm:"column:updated_at" json:"updatedAt"`
}
type RiskLog struct {
	ID          int64     `gorm:"column:id;primaryKey" json:"id,string"`
	UserID      int64     `gorm:"column:user_id" json:"userId,string"`
	TagIDBefore *int64    `gorm:"column:tag_id_before" json:"tagIdBefore,string,omitempty"`
	TagIDAfter  *int64    `gorm:"column:tag_id_after" json:"tagIdAfter,string,omitempty"`
	Source      string    `gorm:"column:source" json:"source"`
	OperatorID  *int64    `gorm:"column:operator_id" json:"operatorId,string,omitempty"`
	Remark      string    `gorm:"column:remark" json:"remark"`
	CreatedAt   time.Time `gorm:"column:created_at" json:"createdAt"`
}

func (v *RiskLog) BeforeCreate(*gorm.DB) error {
	if v.ID == 0 {
		v.ID = snowflake.GenID()
	}
	return nil
}
