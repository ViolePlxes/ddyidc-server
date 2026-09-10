// DTOs for the marketing suite. Snowflake IDs serialize as strings
// to stay within JS safe integers.
package marketing

// InviteDTO 用户端/管理端邀请码视图
type InviteDTO struct {
	ID           string  `json:"id"`
	UserID       string  `json:"userId"`
	Code         string  `json:"code"`
	Status       uint8   `json:"status"`
	RejectReason *string `json:"rejectReason,omitempty"`
	InviteCount  int64   `json:"inviteCount"`
	CreatedAt    string  `json:"createdAt"`
	Username     string  `json:"username,omitempty"`
}

// InviteRelationDTO 邀请关系视图
type InviteRelationDTO struct {
	ID        string `json:"id"`
	InviterID string `json:"inviterId"`
	InviteeID string `json:"inviteeId"`
	Level     uint8  `json:"level"`
	Invitee   string `json:"invitee"`
	Status    uint8  `json:"status"`
	CreatedAt string `json:"createdAt"`
}

// MyInviteResponse 我的邀请码（含推广链接要素）
type MyInviteResponse struct {
	Invite *InviteDTO  `json:"invite,omitempty"`
	Stats  InviteStats `json:"stats"`
}

type InviteStats struct {
	Level1 int64 `json:"level1"`
	Level2 int64 `json:"level2"`
	Level3 int64 `json:"level3"`
}

// RewardDTO 奖励记录视图
type RewardDTO struct {
	ID             string  `json:"id"`
	OrderID        string  `json:"orderId"`
	OrderNo        string  `json:"orderNo"`
	BizType        uint8   `json:"bizType"`
	UserID         string  `json:"userId"`
	Username       string  `json:"username"`
	RewardUserID   string  `json:"rewardUserId"`
	RewardUserName string  `json:"rewardUserName"`
	Level          uint8   `json:"level"`
	RewardType     uint8   `json:"rewardType"`
	BaseAmount     int64   `json:"baseAmount"`
	Amount         int64   `json:"amount"`
	Status         uint8   `json:"status"`
	SettledAt      string  `json:"settledAt,omitempty"`
	Remark         *string `json:"remark,omitempty"`
	CreatedAt      string  `json:"createdAt"`
}

// EarningsResponse 用户收益概览（金额：分）
type EarningsResponse struct {
	Total   int64 `json:"total"`
	Pending int64 `json:"pending"`
	Settled int64 `json:"settled"`
	Frozen  int64 `json:"frozen"`
}

// PromoOverviewResponse 用户推广看板（M3）
type PromoOverviewResponse struct {
	InviteCode    string           `json:"inviteCode"`
	PromoLinkPath string           `json:"promoLinkPath"`
	Registrations int64            `json:"registrations"`
	RechargeTotal int64            `json:"rechargeTotal"`
	SpendTotal    int64            `json:"spendTotal"`
	Earnings      EarningsResponse `json:"earnings"`
	PosterEnabled bool             `json:"posterEnabled"`
}

// CouponDTO 模板视图（管理端/领券中心）
type CouponDTO struct {
	ID           string  `json:"id"`
	Name         string  `json:"name"`
	Type         uint8   `json:"type"`
	FaceValue    int64   `json:"faceValue"`
	DiscountRate float64 `json:"discountRate"`
	MaxDiscount  int64   `json:"maxDiscount"`
	MinAmount    int64   `json:"minAmount"`
	TotalCount   int32   `json:"totalCount"`
	ClaimedCount int64   `json:"claimedCount"`
	UsedCount    int64   `json:"usedCount"`
	PerUserLimit uint32  `json:"perUserLimit"`
	ClaimCode    *string `json:"claimCode,omitempty"`
	ValidDays    uint32  `json:"validDays"`
	EndAt        string  `json:"endAt,omitempty"`
	Status       uint8   `json:"status"`
	CreatedAt    string  `json:"createdAt"`
}

// UserCouponDTO 用户持有券视图
type UserCouponDTO struct {
	ID              string  `json:"id"`
	CouponID        string  `json:"couponId"`
	Code            string  `json:"code"`
	Name            string  `json:"name"`
	Type            uint8   `json:"type"`
	FaceValue       int64   `json:"faceValue"`
	DiscountRate    float64 `json:"discountRate"`
	MaxDiscount     int64   `json:"maxDiscount"`
	MinAmount       int64   `json:"minAmount"`
	Status          uint8   `json:"status"`
	ExpireAt        string  `json:"expireAt"`
	UsedAt          string  `json:"usedAt,omitempty"`
	OrderID         string  `json:"orderId,omitempty"`
	PreviewDiscount int64   `json:"previewDiscount,omitempty"`
}

// ClaimableCouponDTO 领券中心条目
type ClaimableCouponDTO struct {
	CouponDTO
	Remaining   int64 `json:"remaining"`
	UserClaimed int64 `json:"userClaimed"`
}

// SalesUserDTO 销售账号视图
type SalesUserDTO struct {
	ID        string `json:"id"`
	StaffNo   string `json:"staffNo"`
	Username  string `json:"username"`
	Name      string `json:"name"`
	Phone     string `json:"phone,omitempty"`
	Email     string `json:"email,omitempty"`
	Status    uint8  `json:"status"`
	UserID    string `json:"userId,omitempty"`
	CreatedAt string `json:"createdAt"`
}

// VisibilityDTO 字段可见性规则
type VisibilityDTO struct {
	SalesID         string `json:"salesId"`
	CanViewMobile   bool   `json:"canViewMobile"`
	CanViewEmail    bool   `json:"canViewEmail"`
	CanViewOrders   bool   `json:"canViewOrders"`
	CanViewSpend    bool   `json:"canViewSpend"`
	CanViewBalance  bool   `json:"canViewBalance"`
	CanViewRealname bool   `json:"canViewRealname"`
	CanFollowup     bool   `json:"canFollowup"`
}

// SalesCustomerDTO 销售名下客户视图（字段按规则脱敏）
type SalesCustomerDTO struct {
	CustomerID string `json:"customerId"`
	Username   string `json:"username"`
	Mobile     string `json:"mobile"`
	Email      string `json:"email"`
	RealName   string `json:"realName"`
	Status     uint8  `json:"status"`
	CreatedAt  string `json:"createdAt"`
	OrderCount *int64 `json:"orderCount,omitempty"`
	TotalSpend *int64 `json:"totalSpend,omitempty"`
	Balance    *int64 `json:"balance,omitempty"`
	LastPayAt  string `json:"lastPayAt,omitempty"`
}

// FollowupDTO 跟进记录视图
type FollowupDTO struct {
	ID         string `json:"id"`
	SalesID    string `json:"salesId"`
	CustomerID string `json:"customerId"`
	Type       string `json:"type"`
	Content    string `json:"content"`
	CreatedAt  string `json:"createdAt"`
}

// PublicConfigResponse 前端公开配置（注册页/领券中心/订单页用）
type PublicConfigResponse struct {
	InviteEnabled   bool `json:"inviteEnabled"`
	InviteReview    bool `json:"inviteReview"`
	RewardEnabled   bool `json:"rewardEnabled"`
	CouponSelfClaim bool `json:"couponSelfClaim"`
}

// AdminConfigResponse 管理端配置（全部 marketing.* 键）
type AdminConfigResponse struct {
	Items []ConfigItem `json:"items"`
}

// ConfigItem 单条配置
type ConfigItem struct {
	Key         string `json:"key"`
	Value       string `json:"value"`
	Description string `json:"description"`
}
