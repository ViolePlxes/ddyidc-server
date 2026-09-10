package product

import (
	"ddyidc/internal/shared/snowflake"
	"gorm.io/gorm"
	"time"
)

type ProductGroup struct {
	ID        int64     `gorm:"column:id;primaryKey" json:"id,string"`
	TenantID  int64     `gorm:"column:tenant_id" json:"tenantId,string"`
	ParentID  int64     `gorm:"column:parent_id" json:"parentId,string"`
	Name      string    `gorm:"column:name"`
	Icon      string    `gorm:"column:icon"`
	Sort      int       `gorm:"column:sort"`
	Status    uint8     `gorm:"column:status"`
	CreatedAt time.Time `gorm:"column:created_at"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
}

func (ProductGroup) TableName() string { return "product.product_groups" }
func (v *ProductGroup) BeforeCreate(_ *gorm.DB) error {
	if v.ID == 0 {
		v.ID = snowflake.GenID()
	}
	return nil
}

type Product struct {
	ID          int64     `gorm:"column:id;primaryKey" json:"id,string"`
	TenantID    int64     `gorm:"column:tenant_id" json:"tenantId,string"`
	GroupID     int64     `gorm:"column:group_id" json:"groupId,string"`
	Name        string    `gorm:"column:name"`
	ModuleType  string    `gorm:"column:module_type"`
	PayType     uint8     `gorm:"column:pay_type"`
	Stock       *int      `gorm:"column:stock"`
	SoldCount   int       `gorm:"column:sold_count"`
	Status      uint8     `gorm:"column:status"`
	Hidden      bool      `gorm:"column:hidden"`
	Recommended bool      `gorm:"column:recommended"`
	Sort        int       `gorm:"column:sort"`
	Thumbnail   string    `gorm:"column:thumbnail"`
	Summary     string    `gorm:"column:summary"`
	Description string    `gorm:"column:description"`
	CreatedAt   time.Time `gorm:"column:created_at"`
	UpdatedAt   time.Time `gorm:"column:updated_at"`
}

func (Product) TableName() string { return "product.products" }
func (v *Product) BeforeCreate(_ *gorm.DB) error {
	if v.ID == 0 {
		v.ID = snowflake.GenID()
	}
	return nil
}

type ProductConfig struct {
	ID           int64     `gorm:"column:id;primaryKey" json:"id,string"`
	ProductID    int64     `gorm:"column:product_id" json:"productId,string"`
	Name         string    `gorm:"column:name"`
	FieldKey     string    `gorm:"column:field_key"`
	Type         string    `gorm:"column:type"`
	Options      string    `gorm:"column:options"`
	DefaultValue string    `gorm:"column:default_value"`
	Required     bool      `gorm:"column:required"`
	Sort         int       `gorm:"column:sort"`
	Status       uint8     `gorm:"column:status"`
	CreatedAt    time.Time `gorm:"column:created_at"`
	UpdatedAt    time.Time `gorm:"column:updated_at"`
}

func (ProductConfig) TableName() string { return "product.product_configs" }
func (v *ProductConfig) BeforeCreate(_ *gorm.DB) error {
	if v.ID == 0 {
		v.ID = snowflake.GenID()
	}
	return nil
}

// ProductSpec is an operator-maintained sales specification. It is deliberately
// independent from upstream configoptions: configoptions describe provisioning
// choices, while these rows describe the catalogue offer shown to customers.
type ProductSpec struct {
	ID                 int64     `gorm:"column:id;primaryKey" json:"id,string"`
	ProductID          int64     `gorm:"column:product_id" json:"productId,string"`
	Name               string    `gorm:"column:name" json:"name"`
	VCPU               int       `gorm:"column:vcpu" json:"vcpu"`
	Memory             string    `gorm:"column:memory" json:"memory"`
	ProcessorModel     string    `gorm:"column:processor_model" json:"processorModel"`
	ProcessorFrequency string    `gorm:"column:processor_frequency" json:"processorFrequency"`
	Defense            string    `gorm:"column:defense" json:"defense"`
	UpstreamBandwidth  string    `gorm:"column:upstream_bandwidth" json:"upstreamBandwidth"`
	DownstreamRate     string    `gorm:"column:downstream_rate" json:"downstreamRate"`
	Stock              *int      `gorm:"column:stock" json:"stock"`
	PriceMode          string    `gorm:"column:price_mode" json:"priceMode"`
	Price              float64   `gorm:"column:price" json:"price"`
	Sort               int       `gorm:"column:sort" json:"sort"`
	CreatedAt          time.Time `gorm:"column:created_at" json:"createdAt"`
	UpdatedAt          time.Time `gorm:"column:updated_at" json:"updatedAt"`
}

func (ProductSpec) TableName() string { return "product.product_specs" }
func (v *ProductSpec) BeforeCreate(_ *gorm.DB) error {
	if v.ID == 0 {
		v.ID = snowflake.GenID()
	}
	return nil
}
