package model

import (
	"strconv"
	"time"

	"ddyidc/internal/shared/snowflake"
	"gorm.io/gorm"
)

type UpstreamOrder struct {
	ID                string  `gorm:"column:id;primaryKey;type:bigint unsigned" json:"id"`
	OrderID           string  `gorm:"column:order_id;type:bigint unsigned;not null" json:"orderId"`
	SupplierID        string  `gorm:"column:supplier_id;type:bigint unsigned;not null" json:"supplierId"`
	UpstreamProductID int     `gorm:"column:upstream_product_id;not null" json:"upstreamProductId,string"`
	UpstreamOrderID   string  `gorm:"column:upstream_order_id;size:100;not null" json:"upstreamOrderId"`
	UpstreamHostID    string  `gorm:"column:upstream_host_id;size:100" json:"upstreamHostId"`
	Status            string  `gorm:"column:status;size:32;not null" json:"status"`
	Cost              float64 `gorm:"column:cost;type:decimal(12,2);not null" json:"cost"`
	BalanceBefore     float64 `gorm:"column:balance_before;type:decimal(12,2);not null" json:"balanceBefore"`
	BalanceAfter      float64 `gorm:"column:balance_after;type:decimal(12,2);not null" json:"balanceAfter"`
	AttemptCount      int     `gorm:"column:attempt_count;not null" json:"attemptCount"`
	LastError         string  `gorm:"column:last_error;size:512" json:"lastError,omitempty"`
	CreatedAt         int64   `gorm:"column:created_at;autoCreateTime:false" json:"createdAt"`
	UpdatedAt         int64   `gorm:"column:updated_at;autoUpdateTime:false" json:"updatedAt"`
}

func (UpstreamOrder) TableName() string { return "order.upstream_orders" }
func (o *UpstreamOrder) BeforeCreate(_ *gorm.DB) error {
	if o.ID == "" {
		o.ID = strconv.FormatInt(snowflake.GenID(), 10)
	}
	now := time.Now().Unix()
	if o.CreatedAt == 0 {
		o.CreatedAt = now
	}
	o.UpdatedAt = now
	return nil
}
func (o *UpstreamOrder) BeforeUpdate(_ *gorm.DB) error { o.UpdatedAt = time.Now().Unix(); return nil }
