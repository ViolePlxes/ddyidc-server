package customer

import (
	"ddyidc/internal/shared/snowflake"
	"gorm.io/gorm"
	"time"
)

type Customer struct {
	AuthVersion  uint64         `gorm:"column:auth_version;default:1"`
	ID           int64          `gorm:"column:id;primaryKey"`
	TenantID     int64          `gorm:"column:tenant_id"`
	Username     string         `gorm:"column:username"`
	Email        *string        `gorm:"column:email"`
	Phone        *string        `gorm:"column:phone"`
	PasswordHash string         `gorm:"column:password_hash"`
	Nickname     *string        `gorm:"column:nickname"`
	Avatar       *string        `gorm:"column:avatar"`
	Status       uint8          `gorm:"column:status"`
	Level        uint8          `gorm:"column:level"`
	CreatedAt    time.Time      `gorm:"column:created_at"`
	UpdatedAt    time.Time      `gorm:"column:updated_at"`
	DeletedAt    gorm.DeletedAt `gorm:"column:deleted_at"`
}

func (Customer) TableName() string { return "customer.customers" }
func (c *Customer) BeforeCreate(_ *gorm.DB) error {
	if c.ID == 0 {
		c.ID = snowflake.GenID()
	}
	return nil
}
