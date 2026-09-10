package model

// CatalogProduct is the supplier catalogue snapshot. It deliberately remains
// separate from upstream_products: a cached item does not become sellable
// until an administrator maps it to a local SKU.
type CatalogProduct struct {
	ID                string  `gorm:"column:id;primaryKey" json:"id"`
	SupplierID        string  `gorm:"column:supplier_id" json:"supplierId"`
	UpstreamProductID int     `gorm:"column:upstream_product_id" json:"upstreamProductId,string"`
	Name              string  `gorm:"column:name" json:"name"`
	Description       string  `gorm:"column:description" json:"description"`
	Type              string  `gorm:"column:type" json:"type"`
	GroupID           int     `gorm:"column:group_id" json:"groupId,string"`
	PayType           string  `gorm:"column:pay_type" json:"payType"`
	Price             float64 `gorm:"column:price" json:"price"`
	Cycle             string  `gorm:"column:cycle" json:"cycle"`
	CurrencyCode      string  `gorm:"column:currency_code" json:"currencyCode"`
	Status            string  `gorm:"column:status" json:"status"`
	SyncedAt          int64   `gorm:"column:synced_at" json:"syncedAt"`
	CreatedAt         int64   `gorm:"column:created_at" json:"createdAt"`
	UpdatedAt         int64   `gorm:"column:updated_at" json:"updatedAt"`
}

func (CatalogProduct) TableName() string { return "product.upstream_catalog" }

type SyncTask struct {
	ID         string `gorm:"column:id;primaryKey" json:"id"`
	SupplierID string `gorm:"column:supplier_id" json:"supplierId"`
	Type       string `gorm:"column:type" json:"type"`
	Status     string `gorm:"column:status" json:"status"`
	StartedAt  int64  `gorm:"column:started_at" json:"startedAt"`
	FinishedAt int64  `gorm:"column:finished_at" json:"finishedAt"`
	Error      string `gorm:"column:error" json:"error"`
	TriggerBy  string `gorm:"column:trigger_by" json:"triggerBy"`
	Total      int    `gorm:"column:total" json:"total"`
	Progress   int    `gorm:"column:progress" json:"progress"`
}

func (SyncTask) TableName() string { return "system.sync_tasks" }
