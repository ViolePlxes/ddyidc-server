package model

import (
	"strconv"
	"time"

	"ddyidc/internal/shared/snowflake"
	"gorm.io/gorm"
)

type UpstreamHost struct {
	ID             string `gorm:"column:id;primaryKey;type:bigint unsigned" json:"id"`
	HostID         string `gorm:"column:host_id;type:bigint unsigned;not null" json:"hostId"`
	SupplierID     string `gorm:"column:supplier_id;type:bigint unsigned;not null" json:"supplierId"`
	UpstreamHostID string `gorm:"column:upstream_host_id;size:100" json:"upstreamHostId"`
	Status         string `gorm:"column:status;size:32;not null" json:"status"`
	DueTime        int64  `gorm:"column:due_time" json:"dueTime"`
	CreatedAt      int64  `gorm:"column:created_at;autoCreateTime:false" json:"createdAt"`
	UpdatedAt      int64  `gorm:"column:updated_at;autoUpdateTime:false" json:"updatedAt"`
}

func (UpstreamHost) TableName() string { return "instance.upstream_hosts" }
func (h *UpstreamHost) BeforeCreate(_ *gorm.DB) error {
	if h.ID == "" {
		h.ID = strconv.FormatInt(snowflake.GenID(), 10)
	}
	now := time.Now().Unix()
	if h.CreatedAt == 0 {
		h.CreatedAt = now
	}
	h.UpdatedAt = now
	return nil
}
func (h *UpstreamHost) BeforeUpdate(_ *gorm.DB) error { h.UpdatedAt = time.Now().Unix(); return nil }
