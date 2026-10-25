// Package marketing holds the GORM models for the marketing suite (M1-M6).
// Monetary columns are persisted in cents (bigint) to match order/billing.
package marketing

import (
	"time"

	"ddyidc/internal/shared/snowflake"
	"gorm.io/gorm"
)

// Invite is one usable invite code per customer (marketing.invites).
type Invite struct {
	ID           int64      `gorm:"column:id;primaryKey" json:"id,string"`
	UserID       int64      `gorm:"column:user_id" json:"userId,string"`
	Code         string     `gorm:"column:code" json:"code"`
	PendingCode  *string    `gorm:"column:pending_code" json:"pendingCode,omitempty"`
	Status       uint8      `gorm:"column:status" json:"status"` // 0待审核 1启用 2禁用 3已驳回
	RejectReason *string    `gorm:"column:reject_reason" json:"rejectReason"`
	AuditedAt    *time.Time `gorm:"column:audited_at" json:"auditedAt"`
	InviteCount  uint32     `gorm:"column:invite_count" json:"inviteCount"`
	CreatedAt    time.Time  `gorm:"column:created_at" json:"createdAt"`
	UpdatedAt    time.Time  `gorm:"column:updated_at" json:"updatedAt"`
}

func (Invite) TableName() string { return "marketing.invites" }

func (v *Invite) BeforeCreate(_ *gorm.DB) error {
	if v.ID == 0 {
		v.ID = snowflake.GenID()
	}
	return nil
}

// InviteRelation is the inviter tree built on registration (marketing.invite_relations).
type InviteRelation struct {
	ID         int64     `gorm:"column:id;primaryKey" json:"id,string"`
	InviterID  int64     `gorm:"column:inviter_id" json:"inviterId,string"`
	InviteeID  int64     `gorm:"column:invitee_id" json:"inviteeId,string"`
	Level      uint8     `gorm:"column:level" json:"level"`
	InviteCode string    `gorm:"column:invite_code" json:"inviteCode"`
	Status     uint8     `gorm:"column:status" json:"status"` // 1正常 2无效
	CreatedAt  time.Time `gorm:"column:created_at" json:"createdAt"`
	UpdatedAt  time.Time `gorm:"column:updated_at" json:"updatedAt"`
}

func (InviteRelation) TableName() string { return "marketing.invite_relations" }

func (v *InviteRelation) BeforeCreate(_ *gorm.DB) error {
	if v.ID == 0 {
		v.ID = snowflake.GenID()
	}
	return nil
}

// Reward is one distribution payout per order/level (marketing.rewards).
type Reward struct {
	ID           int64      `gorm:"column:id;primaryKey" json:"id,string"`
	OrderID      int64      `gorm:"column:order_id" json:"orderId,string"`
	OrderNo      string     `gorm:"column:order_no" json:"orderNo"`
	BizType      uint8      `gorm:"column:biz_type" json:"bizType"` // 1订单 2充值
	UserID       int64      `gorm:"column:user_id" json:"userId,string"`
	RewardUserID int64      `gorm:"column:reward_user_id" json:"rewardUserId,string"`
	Level        uint8      `gorm:"column:level" json:"level"`
	RewardType   uint8      `gorm:"column:reward_type" json:"rewardType"` // 1百分比 2固定
	BaseAmount   int64      `gorm:"column:base_amount" json:"baseAmount"`
	Amount       int64      `gorm:"column:amount" json:"amount"`
	Status       uint8      `gorm:"column:status" json:"status"` // 0待结算 1已结算 2已冻结 3已回收
	SettledAt    *time.Time `gorm:"column:settled_at" json:"settledAt"`
	Remark       *string    `gorm:"column:remark" json:"remark"`
	CreatedAt    time.Time  `gorm:"column:created_at" json:"createdAt"`
	UpdatedAt    time.Time  `gorm:"column:updated_at" json:"updatedAt"`
}

func (Reward) TableName() string { return "marketing.rewards" }

func (v *Reward) BeforeCreate(_ *gorm.DB) error {
	if v.ID == 0 {
		v.ID = snowflake.GenID()
	}
	return nil
}

// Coupon is the claimable template (marketing.coupons).
type Coupon struct {
	ID           int64      `gorm:"column:id;primaryKey" json:"id,string"`
	Name         string     `gorm:"column:name" json:"name"`
	Type         uint8      `gorm:"column:type" json:"type"` // 1满减 2折扣
	FaceValue    int64      `gorm:"column:face_value" json:"faceValue"`
	DiscountRate float64    `gorm:"column:discount_rate" json:"discountRate"`
	MaxDiscount  int64      `gorm:"column:max_discount" json:"maxDiscount"`
	MinAmount    int64      `gorm:"column:min_amount" json:"minAmount"`
	TotalCount   int32      `gorm:"column:total_count" json:"totalCount"` // -1不限
	ClaimedCount uint32     `gorm:"column:claimed_count" json:"claimedCount"`
	UsedCount    uint32     `gorm:"column:used_count" json:"usedCount"`
	PerUserLimit uint32     `gorm:"column:per_user_limit" json:"perUserLimit"`
	ClaimCode    *string    `gorm:"column:claim_code" json:"claimCode"`
	ValidDays    uint32     `gorm:"column:valid_days" json:"validDays"`
	EndAt        *time.Time `gorm:"column:end_at" json:"endAt"`
	Status       uint8      `gorm:"column:status" json:"status"` // 0停用 1启用
	CreatedAt    time.Time  `gorm:"column:created_at" json:"createdAt"`
	UpdatedAt    time.Time  `gorm:"column:updated_at" json:"updatedAt"`
}

func (Coupon) TableName() string { return "marketing.coupons" }

func (v *Coupon) BeforeCreate(_ *gorm.DB) error {
	if v.ID == 0 {
		v.ID = snowflake.GenID()
	}
	return nil
}

// UserCoupon is a customer-owned coupon instance (marketing.user_coupons).
type UserCoupon struct {
	ID        int64      `gorm:"column:id;primaryKey" json:"id,string"`
	UserID    int64      `gorm:"column:user_id" json:"userId,string"`
	CouponID  int64      `gorm:"column:coupon_id" json:"couponId,string"`
	Code      string     `gorm:"column:code" json:"code"`
	Source    uint8      `gorm:"column:source" json:"source"` // 1自主领取 2定向发放 3领取码
	Status    uint8      `gorm:"column:status" json:"status"` // 0未使用 1已使用 2已过期 3已作废
	OrderID   *int64     `gorm:"column:order_id" json:"orderId,string"`
	UsedAt    *time.Time `gorm:"column:used_at" json:"usedAt"`
	ExpireAt  time.Time  `gorm:"column:expire_at" json:"expireAt"`
	CreatedAt time.Time  `gorm:"column:created_at" json:"createdAt"`
	UpdatedAt time.Time  `gorm:"column:updated_at" json:"updatedAt"`
}

func (UserCoupon) TableName() string { return "marketing.user_coupons" }

func (v *UserCoupon) BeforeCreate(_ *gorm.DB) error {
	if v.ID == 0 {
		v.ID = snowflake.GenID()
	}
	return nil
}

// CouponLog records every coupon lifecycle transition (marketing.coupon_logs).
type CouponLog struct {
	ID           int64     `gorm:"column:id;primaryKey" json:"id,string"`
	CouponID     int64     `gorm:"column:coupon_id" json:"couponId,string"`
	UserCouponID *int64    `gorm:"column:user_coupon_id" json:"userCouponId,string"`
	UserID       *int64    `gorm:"column:user_id" json:"userId,string"`
	Action       uint8     `gorm:"column:action" json:"action"` // 1创建 2发放 3领取 4核销 5停用 6作废 7过期
	OrderID      *int64    `gorm:"column:order_id" json:"orderId,string"`
	Operator     *string   `gorm:"column:operator" json:"operator"`
	Remark       *string   `gorm:"column:remark" json:"remark"`
	CreatedAt    time.Time `gorm:"column:created_at" json:"createdAt"`
}

func (CouponLog) TableName() string { return "marketing.coupon_logs" }

func (v *CouponLog) BeforeCreate(_ *gorm.DB) error {
	if v.ID == 0 {
		v.ID = snowflake.GenID()
	}
	return nil
}

// SalesUser is the sales-staff login account (system.sales_users).
type SalesUser struct {
	ID           int64     `gorm:"column:id;primaryKey" json:"id,string"`
	StaffNo      string    `gorm:"column:staff_no" json:"staffNo"`
	Username     string    `gorm:"column:username" json:"username"`
	PasswordHash string    `gorm:"column:password_hash" json:"-"`
	Name         string    `gorm:"column:name" json:"name"`
	Phone        *string   `gorm:"column:phone" json:"phone"`
	Email        *string   `gorm:"column:email" json:"email"`
	UserID       int64     `gorm:"column:user_id" json:"userId,string"`
	Status       uint8     `gorm:"column:status" json:"status"` // 1启用 2禁用
	CreatedAt    time.Time `gorm:"column:created_at" json:"createdAt"`
	UpdatedAt    time.Time `gorm:"column:updated_at" json:"updatedAt"`
}

func (SalesUser) TableName() string { return "system.sales_users" }

func (v *SalesUser) BeforeCreate(_ *gorm.DB) error {
	if v.ID == 0 {
		v.ID = snowflake.GenID()
	}
	return nil
}

// SalesVisibilityRule controls which customer fields a sales account sees.
// sales_id = 0 is the global default row applied to sales without their own.
type SalesVisibilityRule struct {
	ID              int64     `gorm:"column:id;primaryKey" json:"id,string"`
	SalesID         int64     `gorm:"column:sales_id" json:"salesId,string"`
	CanViewMobile   uint8     `gorm:"column:can_view_mobile" json:"canViewMobile"`
	CanViewEmail    uint8     `gorm:"column:can_view_email" json:"canViewEmail"`
	CanViewOrders   uint8     `gorm:"column:can_view_orders" json:"canViewOrders"`
	CanViewSpend    uint8     `gorm:"column:can_view_spend" json:"canViewSpend"`
	CanViewBalance  uint8     `gorm:"column:can_view_balance" json:"canViewBalance"`
	CanViewRealname uint8     `gorm:"column:can_view_realname" json:"canViewRealname"`
	CanFollowup     uint8     `gorm:"column:can_followup" json:"canFollowup"`
	CreatedAt       time.Time `gorm:"column:created_at" json:"createdAt"`
	UpdatedAt       time.Time `gorm:"column:updated_at" json:"updatedAt"`
}

func (SalesVisibilityRule) TableName() string { return "system.sales_visibility_rules" }

func (v *SalesVisibilityRule) BeforeCreate(_ *gorm.DB) error {
	if v.ID == 0 {
		v.ID = snowflake.GenID()
	}
	return nil
}

// SalesCustomer binds a customer to a sales account (system.sales_customers,
// reuse of the draft table: source is a varchar there).
type SalesCustomer struct {
	ID                int64      `gorm:"column:id;primaryKey" json:"id,string"`
	SalesID           int64      `gorm:"column:sales_id" json:"salesId,string"`
	CustomerID        int64      `gorm:"column:customer_id" json:"customerId,string"`
	Source            string     `gorm:"column:source" json:"source"` // register/admin
	RegisterSalesCode *string    `gorm:"column:register_sales_code" json:"registerSalesCode"`
	AssignedAt        time.Time  `gorm:"column:assigned_at" json:"assignedAt"`
	Status            uint8      `gorm:"column:status" json:"status"` // 1有效 2解绑
	TransferredTo     *int64     `gorm:"column:transferred_to" json:"transferredTo,string"`
	TransferredAt     *time.Time `gorm:"column:transferred_at" json:"transferredAt"`
	CreatedAt         time.Time  `gorm:"column:created_at" json:"createdAt"`
	UpdatedAt         time.Time  `gorm:"column:updated_at" json:"updatedAt"`
}

func (SalesCustomer) TableName() string { return "system.sales_customers" }

func (v *SalesCustomer) BeforeCreate(_ *gorm.DB) error {
	if v.ID == 0 {
		v.ID = snowflake.GenID()
	}
	return nil
}

// SalesFollowup is the placeholder follow-up record module (system.sales_followups).
type SalesFollowup struct {
	ID             int64      `gorm:"column:id;primaryKey" json:"id,string"`
	SalesID        int64      `gorm:"column:sales_id" json:"salesId,string"`
	CustomerID     int64      `gorm:"column:customer_id" json:"customerId,string"`
	FollowupType   string     `gorm:"column:followup_type" json:"followupType"`
	Content        string     `gorm:"column:content" json:"content"`
	NextFollowupAt *time.Time `gorm:"column:next_followup_at" json:"nextFollowupAt"`
	CreatedAt      time.Time  `gorm:"column:created_at" json:"createdAt"`
}

func (SalesFollowup) TableName() string { return "system.sales_followups" }

func (v *SalesFollowup) BeforeCreate(_ *gorm.DB) error {
	if v.ID == 0 {
		v.ID = snowflake.GenID()
	}
	return nil
}

// SalesCustomerRow is the aggregated list row for sales/admin customer views.
type SalesCustomerRow struct {
	CustomerID int64      `json:"customerId,string"`
	Username   string     `json:"username"`
	Email      *string    `json:"email"`
	Phone      *string    `json:"phone"`
	RealName   *string    `json:"realName"`
	Status     uint8      `json:"status"`
	CreatedAt  time.Time  `json:"createdAt"`
	OrderCount int64      `json:"orderCount"`
	TotalSpend int64      `json:"totalSpend"`
	Balance    *int64     `json:"balance"`
	LastPayAt  *time.Time `json:"lastPayAt"`
}

// CustomerOrderRow is the minimal order projection for sales/detail views.
type CustomerOrderRow struct {
	ID             int64      `gorm:"column:id" json:"id,string"`
	OrderNo        string     `gorm:"column:order_no" json:"orderNo"`
	TotalAmount    int64      `gorm:"column:total_amount" json:"totalAmount"`
	DiscountAmount int64      `gorm:"column:discount_amount" json:"discountAmount"`
	CouponAmount   int64      `gorm:"column:coupon_amount" json:"couponAmount"`
	PayAmount      int64      `gorm:"column:pay_amount" json:"payAmount"`
	Status         uint8      `gorm:"column:status" json:"status"`
	CreatedAt      time.Time  `gorm:"column:created_at" json:"createdAt"`
	PaidAt         *time.Time `gorm:"column:paid_at" json:"paidAt"`
}

// Status constants shared by handler/service.
const (
	InvitePending  uint8 = 0
	InviteActive   uint8 = 1
	InviteDisabled uint8 = 2
	InviteRejected uint8 = 3

	RewardPending   uint8 = 0
	RewardSettled   uint8 = 1
	RewardFrozen    uint8 = 2
	RewardReclaimed uint8 = 3

	CouponDisabled uint8 = 0
	CouponEnabled  uint8 = 1

	UserCouponUnused    uint8 = 0
	UserCouponUsed      uint8 = 1
	UserCouponExpired   uint8 = 2
	UserCouponDiscarded uint8 = 3

	OrderStatusPending       uint8 = 1
	OrderStatusPaid          uint8 = 2
	OrderStatusCancelled     uint8 = 3
	OrderStatusRefunded      uint8 = 4
	OrderStatusPartialRefund uint8 = 5
	OrderStatusCompleted     uint8 = 6

	SalesUserActive uint8 = 1
	SalesUserBanned uint8 = 2

	SalesBindingActive  uint8 = 1
	SalesBindingUnbound uint8 = 2

	RelationActive  uint8 = 1
	RelationInvalid uint8 = 2
)

// InviteRelationStatusActive is the normal relation status.
const InviteRelationStatusActive = RelationActive
