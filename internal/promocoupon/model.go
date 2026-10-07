// Package promocoupon funds private coupons for direct platform referrals.
package promocoupon

import (
	balance "ddyidc/internal/billing/balance"
	"encoding/json"
	"time"
)

type Config struct {
	ID                 int             `json:"-"`
	Enabled            bool            `json:"enabled"`
	DirectEnabled      bool            `json:"directEnabled"`
	OfferEnabled       bool            `json:"offerEnabled"`
	PointsEnabled      bool            `json:"pointsEnabled"`
	BalanceEnabled     bool            `json:"balanceEnabled"`
	RenewalEnabled     bool            `json:"renewalEnabled"`
	ReviewRequired     bool            `json:"reviewRequired"`
	PointsPerYuan      int64           `json:"pointsPerYuan"`
	FaceMax            balance.Money   `json:"faceMax"`
	BatchMax           int             `json:"batchMax"`
	DailyCount         int             `json:"dailyCount"`
	DailyBudget        balance.Money   `json:"dailyBudget"`
	ValidDaysMax       int             `json:"validDaysMax"`
	ProductIDs         json.RawMessage `gorm:"column:product_ids" json:"productIds"`
	MaintenanceSeconds int             `json:"maintenanceSeconds"`
	Version            int64           `json:"version"`
	UpdatedAt          time.Time       `json:"updatedAt"`
}

func (Config) TableName() string { return "marketing.promo_coupon_settings" }

type Template struct {
	ID        int64         `json:"id,string"`
	Name      string        `json:"name"`
	Face      balance.Money `json:"face"`
	Minimum   balance.Money `json:"minimum"`
	ValidDays int           `json:"validDays"`
	Enabled   bool          `json:"enabled"`
	UpdatedAt time.Time     `json:"updatedAt"`
}

func (Template) TableName() string { return "marketing.promo_coupon_templates" }

type Batch struct {
	ID              int64           `json:"id,string"`
	OwnerID         int64           `json:"ownerId,string"`
	RequestID       string          `json:"-"`
	RequestHash     string          `json:"-"`
	OrderID         int64           `json:"orderId,string"`
	CouponID        int64           `json:"-"`
	Name            string          `json:"name"`
	Mode            string          `json:"mode"`
	Source          string          `json:"source"`
	Quantity        int             `json:"quantity"`
	Face            balance.Money   `json:"face"`
	Minimum         balance.Money   `json:"minimum"`
	PointCost       int64           `json:"pointCost"`
	ClaimPoints     int64           `json:"claimPoints"`
	BalanceCost     balance.Money   `json:"balanceCost"`
	RefundedPoints  int64           `json:"refundedPoints"`
	RefundedBalance balance.Money   `json:"refundedBalance"`
	RuleSnapshot    json.RawMessage `json:"-"`
	Recipients      json.RawMessage `json:"-"`
	Status          string          `json:"status"`
	ExpiresAt       time.Time       `json:"expiresAt"`
	CreatedAt       time.Time       `json:"createdAt"`
	UpdatedAt       time.Time       `json:"updatedAt"`
}

func (Batch) TableName() string { return "marketing.promo_coupon_batches" }

type Unit struct {
	ID              int64         `json:"id,string"`
	BatchID         int64         `json:"batchId,string"`
	RecipientID     int64         `json:"recipientId,string"`
	UserCouponID    *int64        `json:"userCouponId,string"`
	Status          string        `json:"status"`
	OwnerPoints     int64         `json:"ownerPoints"`
	RecipientPoints int64         `json:"recipientPoints"`
	OwnerBalance    balance.Money `json:"ownerBalance"`
	DebitRef        string        `json:"-"`
	ClaimRef        string        `json:"-"`
	OrderID         *int64        `json:"-"`
	DiscountAmount  balance.Money `json:"discountAmount"`
	CreatedAt       time.Time     `json:"createdAt"`
	UpdatedAt       time.Time     `json:"updatedAt"`
}

func (Unit) TableName() string { return "marketing.promo_coupon_units" }

type Event struct {
	ID         int64           `json:"id,string"`
	BatchID    int64           `json:"batchId,string"`
	UnitID     int64           `json:"unitId,string"`
	ActorID    int64           `json:"actorId,string"`
	ActorRole  string          `json:"actorRole"`
	OperatorIP string          `json:"-"`
	Event      string          `json:"event"`
	RequestID  string          `json:"-"`
	Amount     balance.Money   `json:"amount"`
	Points     int64           `json:"points"`
	Notes      string          `json:"notes"`
	Details    json.RawMessage `json:"details,omitempty"`
	CreatedAt  time.Time       `json:"createdAt"`
}

func (Event) TableName() string { return "marketing.promo_coupon_events" }

type Task struct {
	ID        int64     `json:"id,string"`
	TaskKey   string    `json:"taskKey"`
	BatchID   int64     `json:"batchId,string"`
	Kind      string    `json:"kind"`
	Status    string    `json:"status"`
	Attempts  int       `json:"attempts"`
	NextAt    time.Time `json:"nextAt"`
	LastError string    `json:"lastError"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func (Task) TableName() string { return "marketing.promo_coupon_tasks" }

type Actor struct {
	ID    int64
	Role  string
	IP    string
	Notes string
}
type Input struct {
	TemplateID   int64    `json:"templateId,string"`
	Mode         string   `json:"mode"`
	Source       string   `json:"source"`
	Quantity     int      `json:"quantity"`
	Recipients   []int64  `json:"-"`
	RecipientIDs []string `json:"recipientIds"`
	ClaimPoints  int64    `json:"claimPoints"`
	RequestID    string   `json:"requestId"`
	Quote        string   `json:"quote"`
}
type Quote struct {
	Token          string        `json:"token"`
	ExpiresAt      time.Time     `json:"expiresAt"`
	PointCost      int64         `json:"pointCost"`
	BalanceCost    balance.Money `json:"balanceCost"`
	Quantity       int           `json:"quantity"`
	Face           balance.Money `json:"face"`
	Minimum        balance.Money `json:"minimum"`
	ClaimPoints    int64         `json:"claimPoints"`
	Name           string        `json:"name"`
	ReviewRequired bool          `json:"reviewRequired"`
}
