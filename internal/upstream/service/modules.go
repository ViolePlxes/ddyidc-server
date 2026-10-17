package service

import (
	"context"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/internal/upstream/provider"
	"encoding/json"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"strings"
	"time"
	"unicode/utf8"
)

type ModuleState struct {
	Status   string `json:"status"`
	Revision int64  `json:"revision"`
}
type ModuleView struct {
	Installed bool   `json:"installed"`
	Health    string `json:"health"`
	provider.Module
	ModuleState
	Suppliers     int64 `json:"suppliers"`
	Products      int64 `json:"products"`
	Hosts         int64 `json:"hosts"`
	PendingOrders int64 `json:"pendingOrders"`
	Uninstallable bool  `json:"uninstallable"`
}

func moduleKey(id string) string { return "upstream.module." + id }
func (s *Store) moduleState(id string) (ModuleState, error) {
	state := ModuleState{Status: "enabled"}
	if s.isMemory() {
		s.mu.RLock()
		defer s.mu.RUnlock()
		if v, ok := s.moduleStates[id]; ok {
			return v, nil
		}
		return state, nil
	}
	var row configRow
	err := s.db.Table("system.system_configs").Where("`key`=?", moduleKey(id)).Take(&row).Error
	if err == gorm.ErrRecordNotFound {
		return state, nil
	}
	if err != nil {
		return state, fmt.Errorf("对接能力状态暂不可用")
	}
	if json.Unmarshal([]byte(row.Value), &state) != nil {
		return state, fmt.Errorf("对接能力配置无效")
	}
	switch state.Status {
	case "enabled", "sales_stopped", "disabled":
		return state, nil
	}
	return state, fmt.Errorf("对接能力状态无效")
}
func (s *Store) ModuleViews(ctx context.Context) ([]ModuleView, error) {
	out := []ModuleView{}
	for _, m := range provider.Modules() {
		state, err := s.moduleState(m.ID)
		if err != nil {
			return nil, err
		}
		v := ModuleView{Module: m, ModuleState: state, Installed: true, Health: "not_probed"}
		if s.isMemory() {
			s.mu.RLock()
			for _, a := range s.suppliers {
				if mm, ok := provider.ModuleForVariant(a.Type); ok && mm.ID == m.ID {
					v.Suppliers++
				}
			}
			s.mu.RUnlock()
		} else {
			ids := s.db.WithContext(ctx).Table("common.suppliers").Select("id").Where("type IN ?", m.Variants)
			if err := s.db.WithContext(ctx).Table("common.suppliers").Where("type IN ?", m.Variants).Count(&v.Suppliers).Error; err != nil {
				return nil, err
			}
			for _, c := range []struct {
				table  string
				n      *int64
				filter string
			}{{"product.upstream_products", &v.Products, ""}, {"instance.upstream_hosts", &v.Hosts, ""}, {"order.upstream_orders", &v.PendingOrders, "status NOT IN ('paid','active','cancelled','refunded')"}, {"order.unit_procurements", &v.PendingOrders, "status NOT IN ('paid','active','cancelled','refunded')"}} {
				var n int64
				q := s.db.WithContext(ctx).Table(c.table).Where("supplier_id IN (?)", ids)
				if c.filter != "" {
					q = q.Where(c.filter)
				}
				if err := q.Count(&n).Error; err != nil {
					return nil, fmt.Errorf("对接能力依赖检查失败: %w", err)
				}
				*c.n += n
			}
		}
		// Static Go modules are removed only by a later build after dependency review.
		v.Uninstallable = v.Suppliers == 0 && v.Products == 0 && v.Hosts == 0 && v.PendingOrders == 0 && v.Status == "disabled"
		out = append(out, v)
	}
	return out, nil
}
func moduleAllows(state ModuleState, purpose string) error {
	switch state.Status {
	case "disabled":
		return fmt.Errorf("对接能力已停用，现有业务请联系管理员处理")
	case "sales_stopped":
		if purpose == "sales" {
			return fmt.Errorf("对接能力已停止新销售，现有产品不受停售影响")
		}
	case "enabled":
	default:
		return fmt.Errorf("对接能力状态无效")
	}
	return nil
}
func (s *Store) CheckSupplierCapability(key, purpose string) error {
	v, e := s.GetSupplier(key)
	if e != nil {
		return e
	}
	if v.Status != 1 {
		return fmt.Errorf("供应商已停用")
	}
	m, ok := provider.ModuleForVariant(v.Type)
	if !ok {
		return fmt.Errorf("供应商对接能力未注册")
	}
	state, e := s.moduleState(m.ID)
	if e != nil {
		return e
	}
	if purpose == "sales" && m.Capabilities["purchase"] != "implemented" {
		return fmt.Errorf("此对接能力的新购协议尚未验证，不能开放新销售")
	}
	return moduleAllows(state, purpose)
}
func (s *Store) UpdateModule(ctx context.Context, id, status string, revision, admin int64, ip, notes string) error {
	if admin <= 0 || strings.TrimSpace(ip) == "" || strings.TrimSpace(notes) == "" || utf8.RuneCountInString(notes) > 200 {
		return fmt.Errorf("请填写操作原因")
	}
	if status != "enabled" && status != "sales_stopped" && status != "disabled" {
		return fmt.Errorf("能力状态无效")
	}
	views, e := s.ModuleViews(ctx)
	if e != nil {
		return e
	}
	var target *ModuleView
	for i := range views {
		if views[i].ID == id {
			target = &views[i]
			break
		}
	}
	if target == nil {
		return fmt.Errorf("对接能力不存在")
	}
	// Disable is deliberately conservative: no live dependencies may lose maintenance.
	if status == "disabled" && (target.Suppliers > 0 || target.Products > 0 || target.Hosts > 0 || target.PendingOrders > 0) {
		return fmt.Errorf("仍有供应商、关联商品、机器或待完成订单，请先停止新销售并完成依赖处理")
	}
	if s.isMemory() {
		s.mu.Lock()
		defer s.mu.Unlock()
		current := s.moduleStates[id]
		if current.Revision != revision {
			return fmt.Errorf("状态已变化，请刷新")
		}
		s.moduleStates[id] = ModuleState{Status: status, Revision: revision + 1}
		return nil
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		initial, _ := json.Marshal(ModuleState{Status: "enabled"})
		row := configRow{ID: snowflake.GenID(), Key: moduleKey(id), Value: string(initial), Description: "外部系统对接能力"}
		if e := tx.Table("system.system_configs").Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; e != nil {
			return e
		}
		row.ID = 0
		if e := tx.Table("system.system_configs").Clauses(clause.Locking{Strength: "UPDATE"}).Where("`key`=?", row.Key).Take(&row).Error; e != nil {
			return e
		}
		if status == "disabled" {
			fresh, e := NewStore(tx, s.aesKey).ModuleViews(ctx)
			if e != nil {
				return e
			}
			for _, v := range fresh {
				if v.ID == id && (v.Suppliers > 0 || v.Products > 0 || v.Hosts > 0 || v.PendingOrders > 0) {
					return fmt.Errorf("依赖已变化，不能停用对接能力")
				}
			}
		}
		var current ModuleState
		if json.Unmarshal([]byte(row.Value), &current) != nil || current.Revision != revision {
			return fmt.Errorf("状态已变化，请刷新")
		}
		data, _ := json.Marshal(ModuleState{Status: status, Revision: revision + 1})
		if e := tx.Table("system.system_configs").Where("`key`=?", row.Key).Updates(map[string]any{"value": string(data), "updated_at": time.Now()}).Error; e != nil {
			return e
		}
		detail, _ := json.Marshal(map[string]any{"moduleId": id, "before": current, "after": ModuleState{Status: status, Revision: revision + 1}, "notes": notes})
		return tx.Table("system.audit_logs").Create(map[string]any{"id": snowflake.GenID(), "operator_type": 1, "operator_id": admin, "action": "adapter_state_change", "target_type": "adapter_module", "ip": ip, "detail": string(detail), "created_at": time.Now()}).Error
	})
}

// Transactional admission shares the module row lock with lifecycle updates.
// This prevents a stopped module from accepting an order after a slow quote.
func (s *Store) CheckSupplierCapabilityTx(ctx context.Context, tx *gorm.DB, key, purpose string) error {
	var supplier struct {
		Type   string
		Status int
	}
	if e := tx.WithContext(ctx).Table("common.suppliers").Select("type,status").Where("id=?", key).Take(&supplier).Error; e != nil {
		return e
	}
	if supplier.Status != 1 {
		return fmt.Errorf("供应商已停用")
	}
	m, ok := provider.ModuleForVariant(supplier.Type)
	if !ok {
		return fmt.Errorf("对接能力未注册")
	}
	if purpose == "sales" && m.Capabilities["purchase"] != "implemented" {
		return fmt.Errorf("此对接能力尚未支持新销售")
	}
	initial, _ := json.Marshal(ModuleState{Status: "enabled"})
	row := configRow{ID: snowflake.GenID(), Key: moduleKey(m.ID), Value: string(initial), Description: "外部系统对接能力"}
	if e := tx.WithContext(ctx).Table("system.system_configs").Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; e != nil {
		return e
	}
	row.ID = 0
	if e := tx.WithContext(ctx).Table("system.system_configs").Clauses(clause.Locking{Strength: "SHARE"}).Where("`key`=?", row.Key).Take(&row).Error; e != nil {
		return e
	}
	var state ModuleState
	if json.Unmarshal([]byte(row.Value), &state) != nil {
		return fmt.Errorf("对接能力配置无效")
	}
	return moduleAllows(state, purpose)
}

// ValidateRetainedModules is a read-only deployment/startup gate. A static
// candidate cannot discard a protocol while its supplier identity is retained.
func (s *Store) ValidateRetainedModules(ctx context.Context) error {
	return s.validateRetainedModuleSet(ctx, provider.Modules())
}
func (s *Store) validateRetainedModuleSet(ctx context.Context, modules []provider.Module) error {
	variants := map[string]bool{}
	for _, module := range modules {
		for _, variant := range module.Variants {
			variants[variant] = true
		}
	}
	types := []string{}
	if s.isMemory() {
		s.mu.RLock()
		for _, supplier := range s.suppliers {
			types = append(types, supplier.Type)
		}
		s.mu.RUnlock()
	} else {
		if err := s.db.WithContext(ctx).Table("common.suppliers").Distinct("type").Pluck("type", &types).Error; err != nil {
			return fmt.Errorf("对接能力保留依赖检查失败")
		}
	}
	for _, variant := range types {
		if !variants[variant] {
			return fmt.Errorf("仍有保留供应商使用未安装协议 %q，禁止切换候选版本", variant)
		}
	}
	return nil
}
