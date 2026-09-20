package instance

import (
	"ddyidc/internal/shared/snowflake"
	"gorm.io/gorm"
	"time"
)

// Host maps to instance.instances because the supplied SQL script has no
// separate hosts/host_ips tables.
type Host struct {
	Remark             string         `gorm:"column:remark"`
	OwnershipVersion   int64          `gorm:"column:ownership_version"`
	ID                 int64          `gorm:"column:id;primaryKey"`
	InstanceNo         string         `gorm:"column:instance_no"`
	TenantID           int64          `gorm:"column:tenant_id"`
	CustomerID         int64          `gorm:"column:customer_id"`
	OrderID            int64          `gorm:"column:order_id"`
	OrderItemID        int64          `gorm:"column:order_item_id"`
	ProductID          int64          `gorm:"column:product_id"`
	ProductName        string         `gorm:"column:product_name"`
	ModuleType         string         `gorm:"column:module_type"`
	ConfigSnapshot     string         `gorm:"column:config_snapshot"`
	Status             uint8          `gorm:"column:status"`
	UpstreamProviderID *int64         `gorm:"column:upstream_provider_id"`
	UpstreamInstanceID string         `gorm:"column:upstream_instance_id"`
	PrimaryIP          string         `gorm:"column:primary_ip"`
	SubnetMask         string         `gorm:"column:subnet_mask"`
	Gateway            string         `gorm:"column:gateway"`
	OperatingSystem    string         `gorm:"column:operating_system"`
	PowerStatus        string         `gorm:"column:power_status"`
	LastSyncedAt       *time.Time     `gorm:"column:last_synced_at"`
	CreatedAt          time.Time      `gorm:"column:created_at"`
	ActivatedAt        *time.Time     `gorm:"column:activated_at"`
	ExpiresAt          *time.Time     `gorm:"column:expires_at"`
	UpdatedAt          time.Time      `gorm:"column:updated_at"`
	DeletedAt          gorm.DeletedAt `gorm:"column:deleted_at"`
}

type Event struct {
	ID               int64      `gorm:"column:id;primaryKey"`
	InstanceID       int64      `gorm:"column:instance_id"`
	EventType        string     `gorm:"column:event_type"`
	FromStatus       *uint8     `gorm:"column:from_status"`
	ToStatus         *uint8     `gorm:"column:to_status"`
	OperatorType     uint8      `gorm:"column:operator_type"`
	OperatorID       *int64     `gorm:"column:operator_id"`
	Status           uint8      `gorm:"column:status"`
	UpstreamRequest  string     `gorm:"column:upstream_request"`
	UpstreamResponse string     `gorm:"column:upstream_response"`
	ErrorMessage     string     `gorm:"column:error_msg"`
	CreatedAt        time.Time  `gorm:"column:created_at"`
	ProcessedAt      *time.Time `gorm:"column:processed_at"`
	UpdatedAt        time.Time  `gorm:"column:updated_at"`
}

func (Event) TableName() string { return "instance.instance_events" }
func (v *Event) BeforeCreate(_ *gorm.DB) error {
	if v.ID == 0 {
		v.ID = snowflake.GenID()
	}
	return nil
}

func (Host) TableName() string { return "instance.instances" }
func (v *Host) BeforeCreate(_ *gorm.DB) error {
	if v.ID == 0 {
		v.ID = snowflake.GenID()
	}
	return nil
}
