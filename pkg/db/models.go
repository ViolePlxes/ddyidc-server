package db

import (
	"time"

	"ddyidc/internal/shared/snowflake"
	"gorm.io/gorm"
)

// BaseModel is available to new GORM entities whose table has conventional
// timestamps and soft deletion. Models targeting legacy tables may declare the
// exact columns themselves instead of embedding it.
type BaseModel struct {
	ID        int64          `gorm:"column:id;primaryKey;type:bigint unsigned" json:"id,string"`
	CreatedAt time.Time      `gorm:"column:created_at" json:"createdAt"`
	UpdatedAt time.Time      `gorm:"column:updated_at" json:"updatedAt"`
	DeletedAt gorm.DeletedAt `gorm:"column:deleted_at;index" json:"-"`
}

func (m *BaseModel) BeforeCreate(_ *gorm.DB) error {
	if m.ID == 0 {
		m.ID = snowflake.GenID()
	}
	return nil
}
