package service

// Administration read/write operations stay in the upstream domain so handlers
// never need to know provider or persistence details.

import (
	"fmt"
	"strings"

	productdomain "ddyidc/internal/product/domain"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/internal/upstream/model"
	"gorm.io/gorm"
)

const lossGuardKey = "upstream.loss_guard"

type ProductListQuery struct {
	Page, PageSize            int
	SupplierID, Mode, Keyword string
	NeedManualSync            *bool
}
type AdminProductRow struct {
	model.UpstreamProduct
	GroupName     string  `json:"groupName"`
	ParentGroupID string  `json:"parentGroupId"`
	Cost          float64 `json:"cost"`
	Deletable     bool    `json:"deletable"`
}
type ProductListResult struct {
	Items     []AdminProductRow            `json:"items"`
	Total     int64                        `json:"total"`
	Groups    []productdomain.ProductGroup `json:"groups"`
	LossGuard bool                         `json:"lossGuard"`
}
type UpstreamOrderRow struct {
	ID          string  `json:"id"`
	OrderNo     string  `json:"orderNo"`
	User        string  `json:"user"`
	Email       string  `json:"email"`
	Mobile      string  `json:"mobile"`
	ProductName string  `json:"productName"`
	PayType     string  `json:"payType"`
	Status      string  `json:"status"`
	Amount      float64 `json:"amount"`
	Cost        float64 `json:"cost"`
	Profit      float64 `json:"profit"`
	CreatedAt   string  `json:"createdAt"`
}
type UpstreamOrderMetrics struct {
	Sales        float64 `json:"sales"`
	Profit       float64 `json:"profit"`
	ProductCount int64   `json:"productCount"`
	HostCount    int64   `json:"hostCount"`
}
type UpstreamOrderResult struct {
	Metrics UpstreamOrderMetrics `json:"metrics"`
	Items   []UpstreamOrderRow   `json:"items"`
	Total   int64                `json:"total"`
}

func (s *Store) ListAdminProducts(q ProductListQuery) (ProductListResult, error) {
	if s.isMemory() {
		return ProductListResult{}, fmt.Errorf("upstream administration requires database storage")
	}
	if q.Page < 1 {
		q.Page = 1
	}
	if q.PageSize < 1 || q.PageSize > 100 {
		q.PageSize = 10
	}
	dbq := s.db.Table("product.upstream_products AS up").
		Select(`up.*,
			CASE WHEN parent.id IS NOT NULL THEN CONCAT(parent.name, ' / ', pg.name)
				WHEN pg.id IS NOT NULL THEN pg.name ELSE '未分组' END AS group_name,
			CASE WHEN pg.parent_id > 0 THEN pg.parent_id ELSE 0 END AS parent_group_id,
			up.upstream_price * COALESCE(s.rate, 1) AS cost`).
		Joins("LEFT JOIN product.product_groups AS pg ON pg.id = up.product_group_id").
		Joins("LEFT JOIN product.product_groups AS parent ON parent.id = pg.parent_id").
		Joins("LEFT JOIN common.suppliers AS s ON s.id = up.supplier_id")
	if q.SupplierID != "" && q.SupplierID != "all" {
		dbq = dbq.Where("up.supplier_id = ?", q.SupplierID)
	}
	if q.Mode != "" && q.Mode != "all" {
		dbq = dbq.Where("up.mode = ?", q.Mode)
	}
	if q.NeedManualSync != nil {
		dbq = dbq.Where("up.need_manual_sync = ?", *q.NeedManualSync)
	}
	if word := strings.TrimSpace(q.Keyword); word != "" {
		dbq = dbq.Where("up.name LIKE ? OR CAST(up.upstream_product_id AS CHAR) LIKE ?", "%"+word+"%", "%"+word+"%")
	}
	var total int64
	if err := dbq.Count(&total).Error; err != nil {
		return ProductListResult{}, err
	}
	var items []AdminProductRow
	if err := dbq.Order("up.created_at ASC").Limit(q.PageSize).Offset((q.Page - 1) * q.PageSize).Scan(&items).Error; err != nil {
		return ProductListResult{}, err
	}
	for i := range items {
		if err := s.deleteProtected(items[i].UpstreamProduct); err != nil {
			items[i].Deletable = false
		} else {
			items[i].Deletable = true
		}
	}
	var groups []productdomain.ProductGroup
	if err := s.db.Table((productdomain.ProductGroup{}).TableName()).Order("sort ASC, created_at ASC").Find(&groups).Error; err != nil {
		return ProductListResult{}, err
	}
	guard, err := s.LossGuard()
	if err != nil {
		return ProductListResult{}, err
	}
	return ProductListResult{Items: items, Total: total, Groups: groups, LossGuard: guard}, nil
}

func (s *Store) deleteProtected(product model.UpstreamProduct) error {
	if product.ProductID == "" || product.ProductID == "0" {
		return nil
	}
	var count int64
	// The upstream order link remains present even where a legacy order did not
	// persist an order_item, so it is the authoritative order-retention check.
	if err := s.db.Table("order.upstream_orders AS uo").Joins("JOIN `order`.orders AS o ON o.id = uo.order_id").Where("uo.supplier_id = ? AND uo.upstream_product_id = ? AND o.status NOT IN ?", product.SupplierID, product.UpstreamProductID, []int{3, 4}).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return fmt.Errorf("该商品已有开通产品，禁止删除")
	}
	if err := s.db.Table("instance.instances").Where("product_id = ?", product.ProductID).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return fmt.Errorf("该商品已有开通产品，禁止删除")
	}
	return nil
}

func (s *Store) DeleteProducts(ids []string) error {
	if len(ids) == 0 {
		return fmt.Errorf("请选择要删除的商品")
	}
	if s.isMemory() {
		return fmt.Errorf("upstream administration requires database storage")
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		var products []model.UpstreamProduct
		if err := tx.Table((model.UpstreamProduct{}).TableName()).Where("id IN ?", ids).Find(&products).Error; err != nil {
			return err
		}
		if len(products) != len(ids) {
			return fmt.Errorf("部分商品不存在")
		}
		for _, item := range products {
			if err := s.deleteProtected(item); err != nil {
				return err
			}
		}
		for _, item := range products {
			if item.ProductID != "" && item.ProductID != "0" {
				if err := tx.Table("product.products").Where("id = ?", item.ProductID).Delete(&productdomain.Product{}).Error; err != nil {
					return err
				}
			}
		}
		return tx.Table((model.UpstreamProduct{}).TableName()).Where("id IN ?", ids).Delete(&model.UpstreamProduct{}).Error
	})
}

func (s *Store) SetHidden(id string, hidden bool) error {
	if s.isMemory() {
		return fmt.Errorf("upstream administration requires database storage")
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		var product model.UpstreamProduct
		if err := tx.Table(product.TableName()).Where("id = ?", id).First(&product).Error; err != nil {
			return err
		}
		if err := tx.Table(product.TableName()).Where("id = ?", id).Updates(map[string]any{"hidden": hidden}).Error; err != nil {
			return err
		}
		if product.ProductID != "" && product.ProductID != "0" {
			return tx.Table("product.products").Where("id = ?", product.ProductID).Update("hidden", hidden).Error
		}
		return nil
	})
}
func (s *Store) ListGroups() ([]productdomain.ProductGroup, error) {
	var out []productdomain.ProductGroup
	if s.isMemory() {
		return out, fmt.Errorf("upstream administration requires database storage")
	}
	return out, s.db.Table((productdomain.ProductGroup{}).TableName()).Order("sort ASC, created_at ASC").Find(&out).Error
}
func (s *Store) CreateGroup(name string) (productdomain.ProductGroup, error) {
	v := productdomain.ProductGroup{Name: strings.TrimSpace(name), Status: 1}
	if v.Name == "" {
		return v, fmt.Errorf("分组名称不能为空")
	}
	if s.isMemory() {
		return v, fmt.Errorf("upstream administration requires database storage")
	}
	return v, s.db.Table(v.TableName()).Create(&v).Error
}
func (s *Store) DeleteGroup(id string) error {
	if s.isMemory() {
		return fmt.Errorf("upstream administration requires database storage")
	}
	var count int64
	if err := s.db.Table((model.UpstreamProduct{}).TableName()).Where("product_group_id = ?", id).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return fmt.Errorf("分组下存在代理商品，禁止删除")
	}
	return s.db.Table("product.product_groups").Where("id = ?", id).Delete(&productdomain.ProductGroup{}).Error
}

type configRow struct {
	ID          int64  `gorm:"column:id"`
	Key         string `gorm:"column:key"`
	Value       string `gorm:"column:value"`
	Description string `gorm:"column:description"`
}

func (s *Store) LossGuard() (bool, error) {
	if s.isMemory() {
		return false, nil
	}
	var v configRow
	err := s.db.Table("system.system_configs").Where("`key` = ?", lossGuardKey).First(&v).Error
	if err == gorm.ErrRecordNotFound {
		return false, nil
	}
	return v.Value == "true", err
}
func (s *Store) SetLossGuard(enabled bool) error {
	if s.isMemory() {
		return fmt.Errorf("upstream administration requires database storage")
	}
	value := "false"
	if enabled {
		value = "true"
	}
	row := configRow{ID: snowflake.GenID(), Key: lossGuardKey, Value: value, Description: "亏本交易拦截"}
	return s.db.Exec("INSERT INTO system.system_configs (id, `key`, value, description, updated_at) VALUES (?, ?, ?, ?, NOW()) ON DUPLICATE KEY UPDATE value = VALUES(value), updated_at = NOW()", row.ID, row.Key, row.Value, row.Description).Error
}
func (s *Store) ValidateLossGuard(productID string, salePrice float64) error {
	enabled, err := s.LossGuard()
	if err != nil || !enabled {
		return err
	}
	product, err := s.findProduct(productID)
	if err != nil {
		return nil
	}
	if salePrice < product.UpstreamPrice {
		return fmt.Errorf("商品售价低于上游成本，已触发亏本交易拦截")
	}
	return nil
}

func (s *Store) ListAdminOrders(page, pageSize int, keyword, status string) (UpstreamOrderResult, error) {
	if s.isMemory() {
		return UpstreamOrderResult{}, fmt.Errorf("upstream administration requires database storage")
	}
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 10
	}
	base := s.db.Table("order.upstream_orders AS uo").Joins("JOIN `order`.orders AS o ON o.id = uo.order_id").Joins("JOIN customer.customers AS c ON c.id = o.customer_id").Joins("JOIN product.upstream_products AS up ON up.supplier_id = uo.supplier_id AND up.upstream_product_id = uo.upstream_product_id")
	if status == "paid" {
		base = base.Where("o.status IN ?", []int{2, 6})
	}
	if status == "unpaid" {
		base = base.Where("o.status = ?", 1)
	}
	if word := strings.TrimSpace(keyword); word != "" {
		like := "%" + word + "%"
		base = base.Where("CAST(o.id AS CHAR) LIKE ? OR o.order_no LIKE ? OR c.username LIKE ? OR c.email LIKE ? OR c.phone LIKE ? OR up.name LIKE ?", like, like, like, like, like, like)
	}
	var total int64
	if err := base.Count(&total).Error; err != nil {
		return UpstreamOrderResult{}, err
	}
	var rows []UpstreamOrderRow
	if err := base.Select("o.id, o.order_no, c.username AS user, c.email, c.phone AS mobile, COALESCE(up.name, '') AS product_name, COALESCE(o.pay_type, 0) AS pay_type, o.status, o.pay_amount/100.0 AS amount, uo.cost AS cost, o.created_at").Order("o.created_at DESC").Limit(pageSize).Offset((page - 1) * pageSize).Scan(&rows).Error; err != nil {
		return UpstreamOrderResult{}, err
	}
	for i := range rows {
		rows[i].Profit = rows[i].Amount - rows[i].Cost
		rows[i].Status = mapOrderStatus(rows[i].Status)
		rows[i].PayType = mapPayType(rows[i].PayType)
	}
	var metric struct {
		Sales    float64
		Cost     float64
		Products int64
		Hosts    int64
	}
	if err := s.db.Raw("SELECT COALESCE(SUM(o.pay_amount)/100.0,0) sales, COALESCE(SUM(uo.cost),0) cost, (SELECT COUNT(*) FROM product.upstream_products) products, (SELECT COUNT(*) FROM instance.upstream_hosts) hosts FROM order.upstream_orders uo JOIN `order`.orders o ON o.id=uo.order_id JOIN product.upstream_products up ON up.supplier_id=uo.supplier_id AND up.upstream_product_id=uo.upstream_product_id WHERE o.status NOT IN (3,4)").Scan(&metric).Error; err != nil {
		return UpstreamOrderResult{}, err
	}
	return UpstreamOrderResult{Metrics: UpstreamOrderMetrics{Sales: metric.Sales, Profit: metric.Sales - metric.Cost, ProductCount: metric.Products, HostCount: metric.Hosts}, Items: rows, Total: total}, nil
}
func mapOrderStatus(v string) string {
	if v == "2" || v == "6" {
		return "已付款"
	}
	return "未付款"
}
func mapPayType(v string) string {
	switch v {
	case "1":
		return "支付宝"
	case "2":
		return "微信"
	case "3":
		return "余额"
	}
	return "未支付"
}
