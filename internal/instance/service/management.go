package instance

import (
	"context"
	dom "ddyidc/internal/instance/domain"
	orderapi "ddyidc/internal/order/api"
	"ddyidc/internal/remoteconsole"
	"ddyidc/internal/shared/productionlock"
	"ddyidc/internal/upstream/provider/contracts"
	"encoding/json"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"log"
	"strconv"
	"time"
)

// closeDeliveryForBoundHost makes the order state follow an administrator's
// upstream binding so the customer product list and the admin product list
// agree immediately after the binding is saved.
func closeDeliveryForBoundHost(tx *gorm.DB, instanceID, orderID, orderItemID int64) error {
	if err := tx.Exec("UPDATE `order`.delivery_units SET status='complete', updated_at=NOW(3) WHERE instance_id=? AND order_id=? AND status IN ('unknown','failed')", instanceID, orderID).Error; err != nil {
		return err
	}
	if orderItemID > 0 {
		if err := tx.Exec("UPDATE `order`.order_items SET delivery_status=2, updated_at=NOW(3) WHERE id=? AND order_id=?", orderItemID, orderID).Error; err != nil {
			return err
		}
	}
	return orderapi.CompleteDeliveredOrder(tx, orderID)
}
func (s *Service) SetManagement(db *gorm.DB) { s.db = db }

type EditInput struct {
	ProductOnly        bool    `json:"productOnly"`
	UpstreamInstanceID *string `json:"upstreamInstanceId"`
	CreatedAt          *string `json:"createdAt"`
	ActivatedAt        *string `json:"activatedAt"`
}

func (s *Service) Edit(ctx context.Context, customerID, id int64, in EditInput) error {
	if err := s.productionMutationAllowed(ctx, id); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	actor, admin := actionActor(ctx, customerID)
	if actor != 2 || admin <= 0 {
		return ErrForbidden
	}
	if s.db == nil {
		return ErrUnsupported
	}
	h, e := s.owned(ctx, customerID, id)
	if e != nil {
		return e
	}
	if locked, e := s.repo.IsMarketplaceLocked(ctx, id); e != nil {
		return e
	} else if locked {
		return ErrTradingLocked
	}
	if in.ProductOnly && (in.UpstreamInstanceID == nil || *in.UpstreamInstanceID != h.UpstreamInstanceID || in.CreatedAt != nil || in.ActivatedAt != nil) {
		return errors.New("仅修正商品关联时不得改变机器或日期")
	}
	updates := map[string]any{}
	var upstreamProduct *contracts.UpstreamProductDetail
	for key, v := range map[string]*string{"created_at": in.CreatedAt, "activated_at": in.ActivatedAt} {
		if v == nil {
			continue
		}
		t, e := time.Parse(time.RFC3339, *v)
		if e != nil || t.Year() < 1970 || t.Year() > 2100 {
			return errors.New("时间格式无效，请选择日期与时间")
		}
		updates[key] = t
	}
	if in.UpstreamInstanceID != nil {
		target, e := strconv.ParseInt(*in.UpstreamInstanceID, 10, 64)
		if e != nil || target <= 0 {
			return errors.New("上游实例 ID 必须为正整数")
		}
		// An administrator can repair an empty/stale old binding. Resolve by
		// the fixed supplier, never by the old instance or product identity.
		if h.UpstreamProviderID == nil || s.providers == nil {
			return errors.New("供应商不可用")
		}
		provider, err := s.providers.ProviderFor(strconv.FormatInt(*h.UpstreamProviderID, 10))
		if err != nil {
			return errors.New("供应商不可用")
		}
		p, ok := provider.(contracts.InstanceProvider)
		if !ok {
			return ErrUnsupported
		}
		if scoped, ok := p.(interface {
			WithInstanceContext(context.Context) contracts.InstanceProvider
		}); ok {
			p = scoped.WithInstanceContext(ctx)
		}
		detail, e := p.GetInstanceDetail(target)
		if e != nil {
			return errors.New("读取实例失败，未修改绑定")
		}
		if detail == nil || detail.ID != strconv.FormatInt(target, 10) {
			return errors.New("实例身份不匹配")
		}
		productID, err := strconv.Atoi(detail.ProductID)
		if err != nil || productID <= 0 {
			return errors.New("上游未返回有效商品身份，未修改绑定")
		}
		// Reuse the exact scoped provider/account and its 25-second budget.
		resolved, ok := p.(contracts.Provider)
		if !ok {
			return errors.New("供应商未提供商品查询能力，未修改绑定")
		}
		upstreamProduct, err = resolved.GetProductDetail(productID)
		if err != nil || upstreamProduct == nil || upstreamProduct.ID != productID {
			return errors.New("读取实例所属商品失败，未修改绑定")
		}

		if !in.ProductOnly {
			raw, _ := json.Marshal(snapshotFromUpstream(detail))
			updates["upstream_instance_id"] = detail.ID
			updates["config_snapshot"] = string(raw)
			updates["primary_ip"] = detail.PrimaryIP
			updates["subnet_mask"] = detail.SubnetMask
			updates["gateway"] = detail.Gateway
			updates["operating_system"] = detail.OperatingSystem
			updates["status"] = statusCode(detail.Status, h.Status)
			updates["power_status"] = detail.PowerStatus
			updates["expires_at"] = detail.ExpiresAt
			updates["last_synced_at"] = time.Now()
			if in.ActivatedAt == nil && detail.ActivatedAt != nil {
				updates["activated_at"] = detail.ActivatedAt
			}
		}
		if in.ProductOnly {
			updates["upstream_instance_id"] = detail.ID
		}
	}
	if len(updates) == 0 {
		return errors.New("没有需要保存的字段")
	}
	updates["updated_at"] = time.Now()
	e = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Lock the supplier before any consistent reads: a waiter must see the
		// preceding edit/import commit when checking a target binding.
		if _, syncing := updates["upstream_instance_id"]; syncing {
			var supplier struct{ ID int64 }
			if e := tx.Table("common.suppliers").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND status=1", h.UpstreamProviderID).Take(&supplier).Error; e != nil {
				return e
			}
		}
		var current dom.Host
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND customer_id=?", id, customerID).Take(&current).Error; e != nil {
			return ErrNotFound
		}
		if current.OwnershipVersion != h.OwnershipVersion || current.UpstreamInstanceID != h.UpstreamInstanceID || current.ProductID != h.ProductID || current.ModuleType != h.ModuleType || !sameSupplier(current.UpstreamProviderID, h.UpstreamProviderID) {
			return errors.New("实例已变更，请刷新后重试")
		}
		if err := productionlock.Check(tx, id); err != nil {
			return err
		}
		var trading int64
		if e := tx.Table("system.marketplace_listings").Where("product_id=? AND status IN ('pending','on_sale','locked')", id).Count(&trading).Error; e != nil {
			return e
		}
		if trading > 0 {
			return ErrTradingLocked
		}
		var active int64
		if e := tx.Table("system.push_transfers").Where("instance_id=? AND status='pending'", id).Count(&active).Error; e != nil {
			return e
		}
		if active > 0 {
			return errors.New("实例转让中，不能修改")
		}
		if e := tx.Table("instance.instance_renewals r").Joins("JOIN `order`.orders o ON o.id=r.order_id").Where("r.instance_id=? AND r.state NOT IN ? AND o.status IN (1,2)", id, []string{"completed", "cancelled"}).Count(&active).Error; e != nil {
			return e
		}
		if active > 0 && !in.ProductOnly {
			return errors.New("存在未结束的续费订单，不能修改实例")
		}
		if target, ok := updates["upstream_instance_id"]; ok {
			var count int64
			if e := tx.Unscoped().Model(&dom.Host{}).Where("upstream_provider_id=? AND upstream_instance_id=? AND id<>?", current.UpstreamProviderID, target, id).Count(&count).Error; e != nil {
				return e
			}
			if count > 0 {
				return errors.New("该实例已绑定其他本地产品")
			}
			product, err := resolveEditedProduct(tx, current, upstreamProduct)
			if err != nil {
				return err
			}
			if in.ProductOnly && product.ModuleType != current.ModuleType {
				// Imported instances retain their legacy lifecycle marker. Compare
				// the two catalogue modules, without changing that instance marker.
				var previous struct{ ModuleType string }
				if current.ModuleType != "legacy_snapshot" {
					return errors.New("商品模块不一致，请完成原任务后使用完整编辑")
				}
				if err := tx.Table("product.products").Clauses(clause.Locking{Strength: "UPDATE"}).Select("module_type").Where("id=?", current.ProductID).Take(&previous).Error; err != nil {
					return err
				}
				if previous.ModuleType == "" || (previous.ModuleType != product.ModuleType && !legacyCataloguePlaceholder(previous.ModuleType, product.ModuleType)) {
					return errors.New("商品模块不一致，请完成原任务后使用完整编辑")
				}
			}
			updates["product_id"] = product.ID
			updates["product_name"] = upstreamProduct.Name
			updates["module_type"] = product.ModuleType
			if in.ProductOnly {
				updates["module_type"] = current.ModuleType
			}
			if !in.ProductOnly && (target != h.UpstreamInstanceID || product.ID != current.ProductID || product.ModuleType != current.ModuleType) {
				if err := s.refundMutationAllowed(ctx, tx, id); err != nil {
					return err
				}
				updates["ownership_version"] = current.OwnershipVersion + 1
			}
		}
		if e := tx.Model(&current).Updates(updates).Error; e != nil {
			return e
		}
		// Binding an upstream host finishes a delivery that failed before the
		// supplier reported the host. Without this the order keeps showing
		// "provisioning" on the customer page while the admin list already
		// shows the machine as running.
		if value, ok := updates["upstream_instance_id"]; ok && h.OrderID > 0 && fmt.Sprint(value) != "" && fmt.Sprint(value) != h.UpstreamInstanceID {
			if e := closeDeliveryForBoundHost(tx, id, h.OrderID, h.OrderItemID); e != nil {
				return e
			}
		}
		response := map[string]any{"saved": true, "productOnly": in.ProductOnly}
		for _, key := range []string{"upstream_instance_id", "product_id"} {
			if value, ok := updates[key]; ok {
				response[key] = value
			}
		}
		responseJSON, _ := json.Marshal(response)
		now := time.Now()
		return tx.Create(&dom.Event{InstanceID: id, EventType: "edit", OperatorType: 2, OperatorID: &admin, Status: 2, UpstreamRequest: fmt.Sprintf(`{"previousInstanceId":%q,"previousProductId":"%d"}`, h.UpstreamInstanceID, h.ProductID), UpstreamResponse: string(responseJSON), CreatedAt: now, UpdatedAt: now, ProcessedAt: &now}).Error
	})
	if e == nil {
		remoteconsole.RevokeInstance(id)
	}
	return e
}

// A migrated catalogue placeholder has no physical service type to compare.
// Only known provider-backed server types are eligible for metadata-only repair;
// Edit has already verified the unchanged supplier/host and its real product ID.
func legacyCataloguePlaceholder(previous, resolved string) bool {
	if previous != "legacy_snapshot" {
		return false
	}
	switch resolved {
	case "cloud", "dcimcloud", "dcim", "mf_cloud", "mf_dcim":
		return true
	}
	return false
}

type Credentials struct {
	Username      string `json:"username"`
	Password      string `json:"password"`
	Port          string `json:"port"`
	PortIsDefault bool   `json:"portIsDefault,omitempty"`
}

func (s *Service) Credentials(ctx context.Context, customerID, id int64) (*Credentials, error) {
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	h, e := s.owned(ctx, customerID, id)
	if e != nil {
		return nil, e
	}
	if h.Status == 6 {
		return nil, ErrUnsupported
	}
	if e := s.refundMutationAllowed(ctx, s.db, id); e != nil {
		return nil, e
	}
	p, warning := s.providerContext(ctx, h)
	if warning != "" {
		return nil, ErrUnsupported
	}
	upstreamID := parseHostID(h.UpstreamInstanceID)
	detail, e := p.GetInstanceDetail(upstreamID)
	// A single transient supplier read failure must not surface as "cannot read
	// login info": retry once before reporting.
	if e != nil || detail == nil || detail.ID != h.UpstreamInstanceID {
		time.Sleep(300 * time.Millisecond)
		detail, e = p.GetInstanceDetail(upstreamID)
	}
	if e != nil || detail == nil || detail.ID != h.UpstreamInstanceID {
		detailID := ""
		if detail != nil {
			detailID = detail.ID
		}
		log.Printf("CREDENTIALS detail failed instance=%d upstream=%d detail_id=%q err=%v", id, upstreamID, detailID, e)
		return nil, errors.New("读取登录信息失败")
	}
	current, e := s.owned(ctx, customerID, id)
	if e != nil || current.OwnershipVersion != h.OwnershipVersion || current.UpstreamInstanceID != h.UpstreamInstanceID {
		return nil, ErrForbidden
	}
	actor, operator := actionActor(ctx, customerID)
	now := time.Now()
	if e = s.repo.CreateEvent(ctx, &dom.Event{InstanceID: id, EventType: "credentials", OperatorType: actor, OperatorID: &operator, Status: 2, UpstreamRequest: `{}`, UpstreamResponse: `{"viewed":true}`, CreatedAt: now, UpdatedAt: now, ProcessedAt: &now}); e != nil {
		return nil, e
	}
	return &Credentials{Username: detail.Username, Password: detail.Password, Port: detail.Port, PortIsDefault: detail.PortIsDefault}, nil
}
