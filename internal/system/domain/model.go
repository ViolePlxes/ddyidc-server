package system

import (
	"ddyidc/internal/shared/snowflake"
	"gorm.io/gorm"
	"time"
)

type Configuration struct {
	ID          int64     `gorm:"column:id;primaryKey"`
	Key         string    `gorm:"column:key"`
	Value       string    `gorm:"column:value"`
	Description string    `gorm:"column:description"`
	UpdatedAt   time.Time `gorm:"column:updated_at"`
}

func (Configuration) TableName() string { return "system.system_configs" }
func (v *Configuration) BeforeCreate(_ *gorm.DB) error {
	if v.ID == 0 {
		v.ID = snowflake.GenID()
	}
	return nil
}

// SystemLog maps to the audit_logs table present in the supplied SQL script.
type SystemLog struct {
	ID         int64     `gorm:"column:id;primaryKey"`
	AdminID    *int64    `gorm:"column:admin_id"`
	Module     string    `gorm:"column:module"`
	Action     string    `gorm:"column:action"`
	TargetType string    `gorm:"column:target_type"`
	TargetID   *int64    `gorm:"column:target_id"`
	IP         string    `gorm:"column:ip"`
	CreatedAt  time.Time `gorm:"column:created_at"`
}

func (SystemLog) TableName() string { return "system.audit_logs" }
func (v *SystemLog) BeforeCreate(_ *gorm.DB) error {
	if v.ID == 0 {
		v.ID = snowflake.GenID()
	}
	return nil
}
