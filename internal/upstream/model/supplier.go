package model

import (
	"strconv"
	"time"

	"ddyidc/internal/shared/snowflake"
	"gorm.io/gorm"
)

// Supplier stores encrypted upstream credentials. Token and Secret are never
// serialized to an API caller.
type SupplierSnapshotInfo struct {
	Enabled         bool   `json:"enabled"`
	IntervalMinutes int    `json:"intervalMinutes"`
	State           string `json:"state"`
	LastSuccessAt   int64  `json:"lastSuccessAt"`
	LastAttemptAt   int64  `json:"lastAttemptAt"`
	NextRunAt       int64  `json:"nextRunAt"`
	Total           int    `json:"total"`
	Completed       int    `json:"completed"`
	Failed          int    `json:"failed"`
	Message         string `json:"message"`
}

type Supplier struct {
	LastSyncedAt int64                 `gorm:"-" json:"lastSyncedAt"`
	SnapshotSync *SupplierSnapshotInfo `gorm:"-" json:"snapshotSync,omitempty"`
	ID           string                `gorm:"column:id;primaryKey;type:bigint unsigned" json:"id"`
	Name         string                `gorm:"column:name;size:100;not null" json:"name"`
	Type         string                `gorm:"column:type;size:20;not null" json:"type"`
	URL          string                `gorm:"column:url;size:255;not null" json:"url"`
	Username     string                `gorm:"column:username;size:100;not null" json:"username"`
	Token        string                `gorm:"column:token;type:text;not null" json:"-"`
	Secret       string                `gorm:"column:secret;type:text" json:"-"`
	Rate         float64               `gorm:"column:rate;type:decimal(10,4);not null" json:"rate"`
	CurrencyCode string                `gorm:"column:currency_code;size:10" json:"currencyCode"`
	Status       int                   `gorm:"column:status;type:tinyint;not null" json:"status"`
	Remark       string                `gorm:"column:remark;size:500" json:"remark"`
	CreatedAt    int64                 `gorm:"column:created_at;autoCreateTime:false" json:"createdAt"`
	UpdatedAt    int64                 `gorm:"column:updated_at;autoUpdateTime:false" json:"updatedAt"`
}

func (Supplier) TableName() string { return "common.suppliers" }

func (s *Supplier) BeforeCreate(_ *gorm.DB) error {
	if s.ID == "" {
		s.ID = strconv.FormatInt(snowflake.GenID(), 10)
	}
	stamp := time.Now().Unix()
	if s.CreatedAt == 0 {
		s.CreatedAt = stamp
	}
	s.UpdatedAt = stamp
	return nil
}

func (s *Supplier) BeforeUpdate(_ *gorm.DB) error { s.UpdatedAt = time.Now().Unix(); return nil }
