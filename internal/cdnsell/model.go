package cdnsell

import (
	"encoding/json"
	"time"
)

const (
	settingsTable = "system.cdnsell_settings"
	accountsTable = "system.cdnsell_upstream_accounts"
	packagesTable = "system.cdnsell_upstream_packages"
	productsTable = "system.cdnsell_products"
	batchesTable  = "system.cdnsell_batches"
	ordersTable   = "system.cdnsell_orders"
	meterTable    = "system.cdnsell_meter_records"
)

type UpstreamAccount struct {
	ID              int64           `json:"id,string"`
	Name            string          `json:"name"`
	AccessKey       string          `gorm:"-" json:"accessKey,omitempty"`
	SecretKey       string          `gorm:"-" json:"secretKey,omitempty"`
	AKEncrypted     string          `gorm:"column:ak_encrypted" json:"-"`
	SKEncrypted     string          `gorm:"column:sk_encrypted" json:"-"`
	MaskedAccessKey string          `gorm:"-" json:"maskedAccessKey"`
	Status          bool            `json:"status"`
	Sort            int             `json:"sort"`
	LastTestTime    *time.Time      `json:"lastTestTime"`
	LastTestResult  json.RawMessage `json:"lastTestResult"`
	APIMetrics      json.RawMessage `json:"apiMetrics"`
	CreatedAt       time.Time       `json:"createdAt"`
	UpdatedAt       time.Time       `json:"updatedAt"`
}
type UpstreamPackage struct {
	ID            int64     `json:"id,string"`
	AccountID     int64     `json:"accountId,string"`
	Name          string    `json:"name"`
	CapacityBytes int64     `json:"capacityBytes"`
	UsedBytes     int64     `json:"usedBytes"`
	EffectiveTime time.Time `json:"effectiveTime"`
	ExpireTime    time.Time `json:"expireTime"`
	Status        string    `json:"status"`
	Remark        string    `json:"remark"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}
type Product struct {
	ID            int64           `json:"id,string"`
	Name          string          `json:"name"`
	CapacityBytes int64           `json:"capacityBytes"`
	ValidDays     int             `json:"validDays"`
	Price         Yuan            `json:"price"`
	TierPrices    json.RawMessage `json:"tierPrices"`
	MinQty        int             `json:"minQty"`
	MaxQty        int             `json:"maxQty"`
	Description   string          `json:"description"`
	IsHot         bool            `json:"isHot"`
	Status        bool            `json:"status"`
	Sort          int             `json:"sort"`
	CreatedAt     time.Time       `json:"createdAt"`
	UpdatedAt     time.Time       `json:"updatedAt"`
}
type Batch struct {
	ID             int64     `json:"id,string"`
	UserID         int64     `json:"-"`
	ProductID      int64     `json:"productId,string"`
	OrderID        int64     `json:"orderId,string"`
	OriginalBytes  int64     `json:"originalBytes"`
	ConsumedBytes  int64     `json:"consumedBytes"`
	RemainingBytes int64     `json:"remainingBytes"`
	EffectiveTime  time.Time `json:"effectiveTime"`
	ExpireTime     time.Time `json:"expireTime"`
	Status         string    `json:"status"`
	CreatedAt      time.Time `json:"createdAt"`
}
type MeterRecord struct {
	ID             int64     `json:"id,string"`
	UserID         int64     `json:"userId,string"`
	DomainID       int64     `json:"domainId,string"`
	WindowStart    time.Time `json:"windowStart"`
	WindowEnd      time.Time `json:"windowEnd"`
	ConfirmedBytes int64     `json:"confirmedBytes"`
	RevisedBytes   int64     `json:"revisedBytes"`
	Status         string    `json:"status"`
	Source         string    `json:"source"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}
type Capability struct {
	ID         int64      `json:"id,string"`
	APIName    string     `json:"apiName"`
	Capability string     `json:"capability"`
	Note       string     `json:"note"`
	Available  *bool      `json:"available"`
	CheckedAt  *time.Time `json:"checkedAt"`
}
type PurchaseResult struct {
	OrderID int64  `json:"orderId,string"`
	OrderNo string `json:"orderNo"`
	Amount  Yuan   `json:"amount"`
}
type Overview struct {
	DistributedBytes int64        `json:"distributedBytes"`
	DailyTrend       []TrendPoint `json:"dailyTrend" gorm:"-"`
	RemainingBytes   int64        `json:"remainingBytes"`
	UsedBytes        int64        `json:"usedBytes"`
	TodayBytes       int64        `json:"todayBytes"`
	OverageBytes     int64        `json:"overageBytes"`
	ActiveBatches    int64        `json:"activeBatches"`
	ActiveDomains    int64        `json:"activeDomains"`
	ExpiringAt       *time.Time   `json:"expiringAt"`
	Trend            []TrendPoint `json:"trend" gorm:"-"`
}
type TrendPoint struct {
	Time  time.Time `json:"time"`
	Bytes int64     `json:"bytes"`
}
type CustomerSummary struct {
	UserID         int64  `json:"userId,string"`
	Username       string `json:"username"`
	RemainingBytes int64  `json:"remainingBytes"`
	UsedBytes      int64  `json:"usedBytes"`
	ActiveBatches  int64  `json:"activeBatches"`
}

func (v *UpstreamAccount) sanitize() {
	if len(v.AccessKey) > 4 {
		v.MaskedAccessKey = "****" + v.AccessKey[len(v.AccessKey)-4:]
	} else if v.AKEncrypted != "" {
		v.MaskedAccessKey = "已加密保存"
	}
	v.AccessKey = ""
	v.SecretKey = ""
}
