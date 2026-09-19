package specification

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"

	domain "ddyidc/internal/product/domain"
	"ddyidc/internal/upstream/provider/contracts"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Snapshot struct {
	ProductID  int64  `gorm:"primaryKey"`
	Data       string `gorm:"column:data"`
	CapturedAt time.Time
}

func (Snapshot) TableName() string { return "product.product_spec_snapshots" }

type View struct {
	Options       []contracts.ConfigOption `json:"configOptions,omitempty"`
	Spec          domain.ProductSpec       `json:"spec"`
	UpstreamStock any                      `json:"upstreamStock"`
	CapturedAt    *time.Time               `json:"capturedAt"`
	Configured    bool                     `json:"configured"`
}

func EnsureSchema(db *gorm.DB) error {
	return db.Exec(`CREATE TABLE IF NOT EXISTS product.product_spec_snapshots (
 product_id BIGINT UNSIGNED NOT NULL PRIMARY KEY,data JSON NOT NULL,captured_at DATETIME NOT NULL
 ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`).Error
}

// StockValue preserves a supplier's textual stock rather than coercing it to 0.
func StockValue(raw json.RawMessage, numeric int) any {
	var value any
	if len(raw) > 0 && json.Unmarshal(raw, &value) == nil {
		switch v := value.(type) {
		case string:
			if strings.TrimSpace(v) != "" {
				return v
			}
		case float64:
			if v >= 0 {
				return v
			}
		}
	}
	if numeric >= 0 {
		return numeric
	}
	return nil
}

var leadingNumber = regexp.MustCompile(`^\s*(\d+)`)
var priceSuffix = regexp.MustCompile(`[¥￥].*$`)

func clean(value string) string { return strings.TrimSpace(priceSuffix.ReplaceAllString(value, "")) }

// Capture consumes data already fetched during import/sync. Reading an admin
// specification never calls a provider and never stores tokens/passwords.
func Capture(detail *contracts.UpstreamProductDetail, page *contracts.OrderPage, name string, price float64) View {
	spec := domain.ProductSpec{Name: name, PriceMode: "upstream", Price: price}
	if spec.Name == "" {
		spec.Name = detail.Name
	}
	config := detail.Config
	spec.Memory = config["memory"]
	spec.ProcessorModel = config["processor_model"]
	spec.ProcessorFrequency = config["processor_frequency"]
	spec.Defense = config["defense"]
	spec.UpstreamBandwidth = config["upstream_bandwidth"]
	spec.DownstreamRate = config["downstream_rate"]
	spec.VCPU, _ = strconv.Atoi(config["cpu"])
	options := detail.ConfigOptions
	if page != nil && len(page.ConfigOptions) > 0 {
		options = page.ConfigOptions
	}

	for _, option := range options {
		value := option.Default
		if option.OptionType != "qty" && len(option.Options) > 0 {
			value = ""
			for i, choice := range option.Options {
				if strconv.Itoa(choice.ID) == option.Default || (i == 0 && option.Default == "") {
					value = choice.Name
					break
				}
			}
		}
		value = clean(value)
		switch option.Field {
		case "cpu":
			if match := leadingNumber.FindStringSubmatch(value); len(match) > 1 {
				spec.VCPU, _ = strconv.Atoi(match[1])
			}
		case "memory":
			if spec.Memory == "" {
				spec.Memory = value
			}
		case "defense":
			if spec.Defense == "" {
				spec.Defense = value
			}
		case "bandwidth":
			if strings.Contains(option.Name, "流入") || strings.Contains(option.Name, "下行") {
				if spec.DownstreamRate == "" {
					spec.DownstreamRate = value
				}
			} else if spec.UpstreamBandwidth == "" {
				spec.UpstreamBandwidth = value
			}
		}
	}
	return View{Options: options, Spec: spec, UpstreamStock: StockValue(detail.StockValue, detail.Stock)}
}

func Save(db *gorm.DB, productID int64, view View) error {
	view.Spec.ProductID = productID
	raw, err := json.Marshal(view)
	if err != nil {
		return err
	}
	row := Snapshot{ProductID: productID, Data: string(raw), CapturedAt: time.Now()}
	return db.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "product_id"}}, DoUpdates: clause.AssignmentColumns([]string{"data", "captured_at"})}).Create(&row).Error
}

// ProductBase is the already loaded, local catalogue projection.
type ProductBase struct {
	ID    int64
	Name  string
	Stock *int
	Price float64
}

func Read(ctx context.Context, db *gorm.DB, productID int64) (*View, error) {
	var product ProductBase
	err := db.WithContext(ctx).Table("product.products p").Select("p.id,p.name,p.stock,COALESCE(up.price,0) price").Joins("LEFT JOIN product.upstream_products up ON up.product_id=p.id").Where("p.id=?", productID).Take(&product).Error
	if err != nil {
		return nil, err
	}
	views, err := ReadBatch(ctx, db, []ProductBase{product})
	if err != nil {
		return nil, err
	}
	view := views[productID]
	return &view, nil
}

// ReadBatch adds two bounded IN queries, independent of the page's SKU count.
// Both single-product administration and public lists use the same merge rules.
// This path never calls an upstream provider.
func ReadBatch(ctx context.Context, db *gorm.DB, products []ProductBase) (map[int64]View, error) {
	views := make(map[int64]View, len(products))
	if len(products) == 0 {
		return views, nil
	}
	ids := make([]int64, 0, len(products))
	for _, product := range products {
		ids = append(ids, product.ID)
	}
	var snapshots []Snapshot
	if err := db.WithContext(ctx).Where("product_id IN ?", ids).Find(&snapshots).Error; err != nil {
		return nil, err
	}
	var configured []domain.ProductSpec
	if err := db.WithContext(ctx).Where("product_id IN ?", ids).Order("sort,id").Find(&configured).Error; err != nil {
		return nil, err
	}
	saved := map[int64]*Snapshot{}
	for i := range snapshots {
		saved[snapshots[i].ProductID] = &snapshots[i]
	}
	overrides := map[int64]*domain.ProductSpec{}
	for i := range configured {
		if overrides[configured[i].ProductID] == nil {
			overrides[configured[i].ProductID] = &configured[i]
		}
	}
	for _, product := range products {
		view, err := merge(product, saved[product.ID], overrides[product.ID])
		if err != nil {
			return nil, err
		}
		views[product.ID] = *view
	}
	return views, nil
}

func merge(product ProductBase, saved *Snapshot, override *domain.ProductSpec) (*View, error) {
	productID := product.ID
	var err error
	view := &View{Spec: domain.ProductSpec{ProductID: productID, Name: product.Name, PriceMode: "upstream", Price: product.Price}}
	if product.Stock != nil {
		view.UpstreamStock = *product.Stock
	}
	var snapshot Snapshot
	err = gorm.ErrRecordNotFound
	if saved != nil {
		snapshot = *saved
		err = nil
	}
	if err == nil {
		if err = json.Unmarshal([]byte(snapshot.Data), view); err != nil {
			return nil, err
		}
		view.CapturedAt = &snapshot.CapturedAt
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	var configured domain.ProductSpec
	err = gorm.ErrRecordNotFound
	if override != nil {
		configured = *override
		err = nil
	}
	if err == nil {
		base := view.Spec
		if configured.Name == "" {
			configured.Name = base.Name
		}
		if configured.VCPU == 0 {
			configured.VCPU = base.VCPU
		}
		if configured.Memory == "" {
			configured.Memory = base.Memory
		}
		if configured.ProcessorModel == "" {
			configured.ProcessorModel = base.ProcessorModel
		}
		if configured.ProcessorFrequency == "" {
			configured.ProcessorFrequency = base.ProcessorFrequency
		}
		if configured.Defense == "" {
			configured.Defense = base.Defense
		}
		if configured.UpstreamBandwidth == "" {
			configured.UpstreamBandwidth = base.UpstreamBandwidth
		}
		if configured.DownstreamRate == "" {
			configured.DownstreamRate = base.DownstreamRate
		}
		view.Spec = configured
		view.Configured = true
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	if view.Spec.PriceMode == "upstream" {
		view.Spec.Price = product.Price
	}
	view.Spec.ProductID = productID
	return view, nil
}
