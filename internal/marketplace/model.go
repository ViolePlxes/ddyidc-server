package marketplace

import (
	"encoding/json"
	"time"

	"ddyidc/internal/shared/snowflake"
	"gorm.io/gorm"
)

const (
	settingsTable    = "system.marketplace_settings"
	listingsTable    = "system.marketplace_listings"
	pricesTable      = "system.marketplace_listing_prices"
	favoritesTable   = "system.marketplace_favorites"
	ordersTable      = "system.marketplace_orders"
	wantsTable       = "system.marketplace_wants"
	offersTable      = "system.marketplace_want_offers"
	walletTable      = "system.marketplace_wallet"
	flowsTable       = "system.marketplace_flows"
	withdrawalsTable = "system.marketplace_withdrawals"
	reportsTable     = "system.marketplace_reports"
)

type Settings struct {
	AutoConfirmHours        int             `json:"autoConfirmHours"`
	ID                      int64           `gorm:"column:id;primaryKey" json:"id,string"`
	Enabled                 bool            `gorm:"column:enabled" json:"enabled"`
	Maintenance             bool            `gorm:"column:maintenance" json:"maintenance"`
	AuditEnabled            bool            `gorm:"column:audit_enabled" json:"auditEnabled"`
	RequireBackupConfirm    bool            `gorm:"column:require_backup_confirm" json:"requireBackupConfirm"`
	RequireRealnameListing  bool            `gorm:"column:require_realname_listing" json:"requireRealnameListing"`
	RequireRealnamePurchase bool            `gorm:"column:require_realname_purchase" json:"requireRealnamePurchase"`
	RefundEnabled           bool            `gorm:"column:refund_enabled" json:"refundEnabled"`
	ForceSettleAfterWindow  bool            `gorm:"column:force_settle_after_window" json:"forceSettleAfterWindow"`
	WithdrawalAuditEnabled  bool            `gorm:"column:withdrawal_audit_enabled" json:"withdrawalAuditEnabled"`
	Announcement            string          `gorm:"column:announcement" json:"announcement"`
	Agreement               string          `gorm:"column:agreement" json:"agreement"`
	MaintenanceTip          string          `gorm:"column:maintenance_tip" json:"maintenanceTip"`
	FeeType                 string          `gorm:"column:fee_type" json:"feeType"`
	FeeBearer               string          `gorm:"column:fee_bearer" json:"feeBearer"`
	FeeRate                 string          `gorm:"column:fee_rate" json:"feeRate"`
	FeeFixed                string          `gorm:"column:fee_fixed" json:"feeFixed"`
	WithdrawFeeType         string          `gorm:"column:withdraw_fee_type" json:"withdrawFeeType"`
	WithdrawFeeRate         string          `gorm:"column:withdraw_fee_rate" json:"withdrawFeeRate"`
	WithdrawFeeFixed        string          `gorm:"column:withdraw_fee_fixed" json:"withdrawFeeFixed"`
	MaxListingsPerUser      int             `gorm:"column:max_listings_per_user" json:"maxListingsPerUser"`
	NewUserLimitDays        int             `gorm:"column:new_user_limit_days" json:"newUserLimitDays"`
	ListingValidMinutes     int             `gorm:"column:listing_valid_minutes" json:"listingValidMinutes"`
	OrderLockMinutes        int             `gorm:"column:order_lock_minutes" json:"orderLockMinutes"`
	SettleFreezeDays        int             `gorm:"column:settle_freeze_days" json:"settleFreezeDays"`
	RefundWindowDays        int             `gorm:"column:refund_window_days" json:"refundWindowDays"`
	AllowGroups             json.RawMessage `gorm:"column:allow_groups;type:json" json:"allowGroups"`
	CreatedAt               time.Time       `gorm:"column:created_at" json:"createdAt"`
	UpdatedAt               time.Time       `gorm:"column:updated_at" json:"updatedAt"`
}

func (Settings) TableName() string { return settingsTable }

type Listing struct {
	SellerLabel          string          `gorm:"-" json:"sellerLabel,omitempty"`
	RiskFlag             string          `json:"riskFlag,omitempty"`
	ID                   int64           `gorm:"column:id;primaryKey" json:"id,string"`
	ListingNo            string          `gorm:"column:listing_no" json:"listingNo"`
	SellerID             int64           `gorm:"column:seller_id" json:"sellerId,string,omitempty"`
	ProductID            int64           `gorm:"column:product_id" json:"productId,string"`
	SourceProductID      int64           `gorm:"column:source_product_id" json:"sourceProductId,string"`
	GroupID              int64           `gorm:"column:group_id" json:"groupId,string"`
	ProductName          string          `gorm:"column:product_name" json:"productName"`
	ProductType          string          `gorm:"column:product_type" json:"productType"`
	Region               string          `gorm:"column:region" json:"region"`
	ConfigSnapshot       json.RawMessage `gorm:"column:config_snapshot;type:json" json:"configSnapshot"`
	Price                string          `gorm:"column:price" json:"price"`
	OriginalRenewalPrice string          `gorm:"column:original_renewal_price" json:"originalRenewalPrice"`
	RemainingDays        int             `gorm:"column:remaining_days" json:"remainingDays"`
	Status               string          `gorm:"column:status" json:"status"`
	AuditStatus          string          `gorm:"column:audit_status" json:"auditStatus"`
	AuditRemark          string          `gorm:"column:audit_remark" json:"auditRemark,omitempty"`
	AuditorID            *int64          `gorm:"column:auditor_id" json:"auditorId,string,omitempty"`
	Views                int             `gorm:"column:views" json:"views"`
	FavoriteCount        int             `gorm:"column:favorite_count" json:"favoriteCount"`
	SellerNote           string          `gorm:"column:seller_note" json:"sellerNote"`
	IsRecommended        bool            `gorm:"column:is_recommended" json:"isRecommended"`
	ExpiresAt            time.Time       `gorm:"column:expires_at" json:"expiresAt"`
	CreatedAt            time.Time       `gorm:"column:created_at" json:"createdAt"`
	UpdatedAt            time.Time       `gorm:"column:updated_at" json:"updatedAt"`
	Favorite             bool            `gorm:"-" json:"favorite,omitempty"`
	PriceHistory         []ListingPrice  `gorm:"-" json:"priceHistory,omitempty"`
}

func (Listing) TableName() string { return listingsTable }
func (v *Listing) BeforeCreate(*gorm.DB) error {
	if v.ID == 0 {
		v.ID = snowflake.GenID()
	}
	return nil
}

type ListingPrice struct {
	ID        int64     `gorm:"column:id;primaryKey" json:"id,string"`
	ListingID int64     `gorm:"column:listing_id" json:"listingId,string"`
	OldPrice  string    `gorm:"column:old_price" json:"oldPrice"`
	NewPrice  string    `gorm:"column:new_price" json:"newPrice"`
	CreatedAt time.Time `gorm:"column:created_at" json:"createdAt"`
}

func (ListingPrice) TableName() string { return pricesTable }
func (v *ListingPrice) BeforeCreate(*gorm.DB) error {
	if v.ID == 0 {
		v.ID = snowflake.GenID()
	}
	return nil
}

type Order struct {
	RefundApplicationID int64      `gorm:"-" json:"refundApplicationId,string,omitempty"`
	RefundPending       bool       `gorm:"-" json:"refundPending"`
	SettlementMode      string     `json:"settlementMode"`
	OperatorIP          string     `json:"-"`
	ID                  int64      `gorm:"column:id;primaryKey" json:"id,string"`
	OrderNo             string     `gorm:"column:order_no" json:"orderNo"`
	ListingID           int64      `gorm:"column:listing_id" json:"listingId,string"`
	BuyerID             int64      `gorm:"column:buyer_id" json:"buyerId,string"`
	SellerID            int64      `gorm:"column:seller_id" json:"sellerId,string"`
	Amount              string     `gorm:"column:amount" json:"amount"`
	PayAmount           string     `gorm:"column:pay_amount" json:"payAmount"`
	Fee                 string     `gorm:"column:fee" json:"fee"`
	FeeBearer           string     `gorm:"column:fee_bearer" json:"feeBearer"`
	SellerIncome        string     `gorm:"column:seller_income" json:"sellerIncome"`
	Status              string     `gorm:"column:status" json:"status"`
	PaymentMethod       string     `gorm:"column:payment_method" json:"paymentMethod"`
	LockExpiresAt       time.Time  `gorm:"column:lock_expires_at" json:"lockExpiresAt"`
	PaidAt              *time.Time `gorm:"column:paid_at" json:"paidAt,omitempty"`
	SettleAt            *time.Time `gorm:"column:settle_at" json:"settleAt,omitempty"`
	RefundWindowEnd     *time.Time `gorm:"column:refund_window_end" json:"refundWindowEnd,omitempty"`
	CreatedAt           time.Time  `gorm:"column:created_at" json:"createdAt"`
	UpdatedAt           time.Time  `gorm:"column:updated_at" json:"updatedAt"`
	Listing             *Listing   `gorm:"-" json:"listing,omitempty"`
}

func (Order) TableName() string { return ordersTable }
func (v *Order) BeforeCreate(*gorm.DB) error {
	if v.ID == 0 {
		v.ID = snowflake.GenID()
	}
	return nil
}

type Want struct {
	ID          int64           `gorm:"column:id;primaryKey" json:"id,string"`
	WantNo      string          `gorm:"column:want_no" json:"wantNo"`
	UserID      int64           `gorm:"column:user_id" json:"userId,string,omitempty"`
	Title       string          `gorm:"column:title" json:"title"`
	BudgetMin   string          `gorm:"column:budget_min" json:"budgetMin"`
	BudgetMax   string          `gorm:"column:budget_max" json:"budgetMax"`
	Region      string          `gorm:"column:region" json:"region"`
	Description string          `gorm:"column:description" json:"description"`
	Tags        json.RawMessage `gorm:"column:tags;type:json" json:"tags"`
	Status      string          `gorm:"column:status" json:"status"`
	CreatedAt   time.Time       `gorm:"column:created_at" json:"createdAt"`
	UpdatedAt   time.Time       `gorm:"column:updated_at" json:"updatedAt"`
}

func (Want) TableName() string { return wantsTable }
func (v *Want) BeforeCreate(*gorm.DB) error {
	if v.ID == 0 {
		v.ID = snowflake.GenID()
	}
	return nil
}

type WantOffer struct {
	ID        int64     `gorm:"column:id;primaryKey" json:"id,string"`
	WantID    int64     `gorm:"column:want_id" json:"wantId,string"`
	SellerID  int64     `gorm:"column:seller_id" json:"sellerId,string"`
	ListingID *int64    `gorm:"column:listing_id" json:"listingId,string,omitempty"`
	Price     string    `gorm:"column:price" json:"price"`
	Message   string    `gorm:"column:message" json:"message"`
	CreatedAt time.Time `gorm:"column:created_at" json:"createdAt"`
}

func (WantOffer) TableName() string { return offersTable }
func (v *WantOffer) BeforeCreate(*gorm.DB) error {
	if v.ID == 0 {
		v.ID = snowflake.GenID()
	}
	return nil
}

type Wallet struct {
	UserID         int64     `gorm:"column:user_id;primaryKey" json:"userId,string"`
	Balance        string    `gorm:"column:balance" json:"balance"`
	FrozenBalance  string    `gorm:"column:frozen_balance" json:"frozenBalance"`
	TotalIncome    string    `gorm:"column:total_income" json:"totalIncome"`
	TotalWithdrawn string    `gorm:"column:total_withdrawn" json:"totalWithdrawn"`
	UpdatedAt      time.Time `gorm:"column:updated_at" json:"updatedAt"`
}

func (Wallet) TableName() string { return walletTable }

type Flow struct {
	ID           int64     `gorm:"column:id;primaryKey" json:"id,string"`
	FlowNo       string    `gorm:"column:flow_no" json:"flowNo"`
	UserID       int64     `gorm:"column:user_id" json:"userId,string"`
	Type         string    `gorm:"column:type" json:"type"`
	Direction    string    `gorm:"column:direction" json:"direction"`
	Amount       string    `gorm:"column:amount" json:"amount"`
	BalanceAfter string    `gorm:"column:balance_after" json:"balanceAfter"`
	RefID        int64     `gorm:"column:ref_id" json:"refId,string"`
	Remark       string    `gorm:"column:remark" json:"remark"`
	CreatedAt    time.Time `gorm:"column:created_at" json:"createdAt"`
}

func (Flow) TableName() string { return flowsTable }
func (v *Flow) BeforeCreate(*gorm.DB) error {
	if v.ID == 0 {
		v.ID = snowflake.GenID()
	}
	return nil
}

type Withdrawal struct {
	ID           int64     `gorm:"column:id;primaryKey" json:"id,string"`
	WithdrawNo   string    `gorm:"column:withdraw_no" json:"withdrawNo"`
	UserID       int64     `gorm:"column:user_id" json:"userId,string"`
	Amount       string    `gorm:"column:amount" json:"amount"`
	Fee          string    `gorm:"column:fee" json:"fee"`
	ActualAmount string    `gorm:"column:actual_amount" json:"actualAmount"`
	Status       string    `gorm:"column:status" json:"status"`
	AuditorID    *int64    `gorm:"column:auditor_id" json:"auditorId,string,omitempty"`
	Remark       string    `gorm:"column:remark" json:"remark"`
	CreatedAt    time.Time `gorm:"column:created_at" json:"createdAt"`
	UpdatedAt    time.Time `gorm:"column:updated_at" json:"updatedAt"`
}

func (Withdrawal) TableName() string { return withdrawalsTable }
func (v *Withdrawal) BeforeCreate(*gorm.DB) error {
	if v.ID == 0 {
		v.ID = snowflake.GenID()
	}
	return nil
}

type Report struct {
	ID         int64     `gorm:"column:id;primaryKey" json:"id,string"`
	ListingID  int64     `gorm:"column:listing_id" json:"listingId,string"`
	ReporterID int64     `gorm:"column:reporter_id" json:"reporterId,string"`
	Reason     string    `gorm:"column:reason" json:"reason"`
	Detail     string    `gorm:"column:detail" json:"detail"`
	Status     string    `gorm:"column:status" json:"status"`
	HandlerID  *int64    `gorm:"column:handler_id" json:"handlerId,string,omitempty"`
	CreatedAt  time.Time `gorm:"column:created_at" json:"createdAt"`
	UpdatedAt  time.Time `gorm:"column:updated_at" json:"updatedAt"`
}

func (Report) TableName() string { return reportsTable }
func (v *Report) BeforeCreate(*gorm.DB) error {
	if v.ID == 0 {
		v.ID = snowflake.GenID()
	}
	return nil
}
