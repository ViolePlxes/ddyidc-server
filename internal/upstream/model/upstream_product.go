package model

import (
	"strconv"
	"time"

	"ddyidc/internal/shared/snowflake"
	"gorm.io/gorm"
)

type UpstreamProduct struct {
	ID                 string  `gorm:"column:id;primaryKey;type:bigint unsigned" json:"id"`
	SupplierID         string  `gorm:"column:supplier_id;type:bigint unsigned;not null" json:"supplierId"`
	UpstreamProductID  int     `gorm:"column:upstream_product_id;not null" json:"upstreamProductId,string"`
	ProductID          string  `gorm:"column:product_id;type:bigint unsigned" json:"productId"`
	Name               string  `gorm:"column:name;size:200;not null" json:"name"`
	Description        string  `gorm:"column:description;type:text" json:"description"`
	ProductGroupID     string  `gorm:"column:product_group_id;type:bigint unsigned" json:"productGroupId"`
	ProfitType         int     `gorm:"column:profit_type;not null" json:"profitType"`
	ProfitValue        float64 `gorm:"column:profit_value;type:decimal(10,2);not null" json:"profitValue"`
	RenewProfitType    int     `gorm:"column:renew_profit_type;not null" json:"renewProfitType"`
	RenewProfitValue   float64 `gorm:"column:renew_profit_value;type:decimal(10,2);not null" json:"renewProfitValue"`
	UpgradeProfitType  int     `gorm:"column:upgrade_profit_type;not null" json:"upgradeProfitType"`
	UpgradeProfitValue float64 `gorm:"column:upgrade_profit_value;type:decimal(10,2);not null" json:"upgradeProfitValue"`
	AutoSetup          bool    `gorm:"column:auto_setup;not null" json:"autoSetup"`
	Hidden             bool    `gorm:"column:hidden;not null" json:"hidden"`
	Mode               string  `gorm:"column:mode;size:20;not null" json:"mode"`
	PriceBasis         string  `gorm:"column:price_basis;size:20;not null" json:"priceBasis"`
	NeedManualSync     bool    `gorm:"column:need_manual_sync;not null" json:"needManualSync"`
	UpstreamPrice      float64 `gorm:"column:upstream_price;type:decimal(12,2)" json:"upstreamPrice"`
	Price              float64 `gorm:"column:price;type:decimal(12,2)" json:"price"`
	Cycle              string  `gorm:"column:cycle;size:50" json:"cycle"`
	PayType            string  `gorm:"column:pay_type;size:50" json:"payType"`
	CreatedAt          int64   `gorm:"column:created_at;autoCreateTime:false" json:"createdAt"`
	UpdatedAt          int64   `gorm:"column:updated_at;autoUpdateTime:false" json:"updatedAt"`
}

func (UpstreamProduct) TableName() string { return "product.upstream_products" }
func (p *UpstreamProduct) BeforeCreate(_ *gorm.DB) error {
	if p.ID == "" {
		p.ID = strconv.FormatInt(snowflake.GenID(), 10)
	}
	now := time.Now().Unix()
	if p.CreatedAt == 0 {
		p.CreatedAt = now
	}
	p.UpdatedAt = now
	return nil
}
func (p *UpstreamProduct) BeforeUpdate(_ *gorm.DB) error { p.UpdatedAt = time.Now().Unix(); return nil }
