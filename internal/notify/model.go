package notify

import (
	"time"

	"ddyidc/internal/shared/snowflake"
	"gorm.io/gorm"
)

const (
	ChannelSMS   = "sms"
	ChannelEmail = "email"
	ChannelBoth  = "both"

	StatusSuccess = "success"
	StatusFailed  = "failed"
)

type NotificationTemplate struct {
	ID        int64     `gorm:"column:id;primaryKey" json:"id,string"`
	Scene     string    `gorm:"column:scene" json:"scene"`
	Channel   string    `gorm:"column:channel" json:"channel"`
	Title     string    `gorm:"column:title" json:"title"`
	Content   string    `gorm:"column:content" json:"content"`
	Enabled   bool      `gorm:"column:enabled" json:"enabled"`
	CreatedAt time.Time `gorm:"column:created_at" json:"createdAt"`
	UpdatedAt time.Time `gorm:"column:updated_at" json:"updatedAt"`
}

func (NotificationTemplate) TableName() string { return "system.notification_templates" }
func (v *NotificationTemplate) BeforeCreate(_ *gorm.DB) error {
	if v.ID == 0 {
		v.ID = snowflake.GenID()
	}
	return nil
}

type NotifyLog struct {
	ID              int64     `gorm:"column:id;primaryKey" json:"id,string"`
	Scene           string    `gorm:"column:scene" json:"scene"`
	Channel         string    `gorm:"column:channel" json:"channel"`
	Target          string    `gorm:"column:target" json:"target"`
	TemplateID      *int64    `gorm:"column:template_id" json:"templateId,omitempty"`
	ContentSnapshot string    `gorm:"column:content_snapshot" json:"contentSnapshot"`
	Status          string    `gorm:"column:status" json:"status"`
	Error           string    `gorm:"column:error" json:"error,omitempty"`
	RequestID       string    `gorm:"column:request_id" json:"requestId,omitempty"`
	BusinessID      string    `gorm:"column:business_id" json:"businessId,omitempty"`
	CreatedAt       time.Time `gorm:"column:created_at" json:"createdAt"`
}

func (NotifyLog) TableName() string { return "system.notify_logs" }
func (v *NotifyLog) BeforeCreate(_ *gorm.DB) error {
	if v.ID == 0 {
		v.ID = snowflake.GenID()
	}
	return nil
}

type Contact struct {
	SkipSMS   bool   `gorm:"column:skip_sms"`
	SkipEmail bool   `gorm:"column:skip_email"`
	Email     string `gorm:"column:email"`
	Phone     string `gorm:"column:phone"`
}

type ExpiringInstance struct {
	PrimaryIP   string     `gorm:"column:primary_ip"`
	ID          int64      `gorm:"column:id"`
	CustomerID  int64      `gorm:"column:customer_id"`
	InstanceNo  string     `gorm:"column:instance_no"`
	ProductName string     `gorm:"column:product_name"`
	ExpiresAt   *time.Time `gorm:"column:expires_at"`
}
