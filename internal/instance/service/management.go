package instance

import (
	"context"
	dom "ddyidc/internal/instance/domain"
	"ddyidc/internal/remoteconsole"
	"encoding/json"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"strconv"
	"time"
)

func (s *Service) SetManagement(db *gorm.DB) { s.db = db }

type EditInput struct {
	UpstreamInstanceID *string `json:"upstreamInstanceId"`
	CreatedAt          *string `json:"createdAt"`
	ActivatedAt        *string `json:"activatedAt"`
}

func (s *Service) Edit(ctx context.Context, customerID, id int64, in EditInput) error {
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
	updates := map[string]any{}
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
		p, warning := s.providerContext(ctx, h)
		if warning != "" {
			return errors.New("供应商不可用")
		}
		detail, e := p.GetInstanceDetail(target)
		if e != nil {
			return errors.New("读取实例失败，未修改绑定")
		}
		if detail == nil || detail.ID != strconv.FormatInt(target, 10) {
			return errors.New("实例身份不匹配")
		}
		var mapping struct{ UpstreamProductID int64 }
		if e = s.db.Table("product.upstream_products").Select("upstream_product_id").Where("product_id=? AND supplier_id=?", h.ProductID, h.UpstreamProviderID).Take(&mapping).Error; e != nil {
			return e
		}
		if detail.ProductID != strconv.FormatInt(mapping.UpstreamProductID, 10) {
			return errors.New("实例所属商品与当前产品不一致")
		}
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
	if len(updates) == 0 {
		return errors.New("没有需要保存的字段")
	}
	updates["updated_at"] = time.Now()
	e = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current dom.Host
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND customer_id=?", id, customerID).Take(&current).Error; e != nil {
			return ErrNotFound
		}
		if current.OwnershipVersion != h.OwnershipVersion || current.UpstreamInstanceID != h.UpstreamInstanceID {
			return errors.New("实例已变更，请刷新后重试")
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
		if active > 0 {
			return errors.New("存在未结束的续费订单，不能修改实例")
		}
		if target, ok := updates["upstream_instance_id"]; ok {
			var supplier struct{ ID int64 }
			if e := tx.Table("common.suppliers").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", current.UpstreamProviderID).Take(&supplier).Error; e != nil {
				return e
			}
			var count int64
			if e := tx.Unscoped().Model(&dom.Host{}).Where("upstream_provider_id=? AND upstream_instance_id=? AND id<>?", current.UpstreamProviderID, target, id).Count(&count).Error; e != nil {
				return e
			}
			if count > 0 {
				return errors.New("该实例已绑定其他本地产品")
			}
			if target != h.UpstreamInstanceID {
				updates["ownership_version"] = current.OwnershipVersion + 1
			}
		}
		if e := tx.Model(&current).Updates(updates).Error; e != nil {
			return e
		}
		now := time.Now()
		return tx.Create(&dom.Event{InstanceID: id, EventType: "edit", OperatorType: 2, OperatorID: &admin, Status: 2, UpstreamRequest: fmt.Sprintf(`{"previousInstanceId":%q}`, h.UpstreamInstanceID), UpstreamResponse: `{"saved":true}`, CreatedAt: now, UpdatedAt: now, ProcessedAt: &now}).Error
	})
	if e == nil {
		remoteconsole.RevokeInstance(id)
	}
	return e
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
	detail, e := p.GetInstanceDetail(parseHostID(h.UpstreamInstanceID))
	if e != nil || detail == nil || detail.ID != h.UpstreamInstanceID {
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
