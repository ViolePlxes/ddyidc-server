package instance

import (
	"context"
	dom "ddyidc/internal/instance/domain"
	"ddyidc/internal/instance/readmodel"
	"ddyidc/internal/shared/workgate"
	"ddyidc/internal/upstream/provider/contracts"
	upstream "ddyidc/internal/upstream/service"
	"encoding/json"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"reflect"
	"time"
)

func (s *Service) withInstanceReadLock(ctx context.Context, id int64, fn func() error) error {
	pool, e := s.db.DB()
	if e != nil {
		return e
	}
	conn, e := pool.Conn(ctx)
	if e != nil {
		return e
	}
	defer conn.Close()
	name := fmt.Sprintf("ddyidc:instance-read:%d", id)
	var acquired int
	if e = conn.QueryRowContext(ctx, "SELECT GET_LOCK(?,35)", name).Scan(&acquired); e != nil || acquired != 1 {
		return errors.New("实例信息同步中，请稍后重试")
	}
	defer func() {
		call, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, _ = conn.ExecContext(call, "SELECT RELEASE_LOCK(?)", name)
	}()
	return fn()
}

// Also compare facts when MySQL's DATETIME timestamp has only second precision.
func sameSyncReadVersion(a, b dom.Host) bool {
	return a.UpdatedAt.Equal(b.UpdatedAt) && a.ConfigSnapshot == b.ConfigSnapshot && a.PrimaryIP == b.PrimaryIP && a.PowerStatus == b.PowerStatus && a.Status == b.Status && a.CustomerID == b.CustomerID && a.ProductID == b.ProductID && a.OwnershipVersion == b.OwnershipVersion && reflect.DeepEqual(a.UpstreamProviderID, b.UpstreamProviderID) && a.UpstreamInstanceID == b.UpstreamInstanceID && reflect.DeepEqual(a.ExpiresAt, b.ExpiresAt)
}
func (s *Service) snapshotRenewalOptions(ctx context.Context, h *dom.Host) ([]RenewalOption, error) {
	return s.RenewalOptions(ctx, h.CustomerID, h.ID)
}
func applySupplierReadSnapshot(h *dom.Host, detail *contracts.InstanceDetail, fields map[string]any, options []RenewalOption, quoteErr error, now time.Time) error {
	if h.UpstreamProviderID == nil {
		return errors.New("实例未关联供应商")
	}
	cycle := detail.BillingCycle
	if cycle == "" {
		cycle = listItem(h).BillingCycle
	}
	var selected *RenewalOption
	if quoteErr == nil {
		for i := range options {
			if options[i].Cycle == cycle {
				selected = &options[i]
				break
			}
		}
	}
	var outer map[string]json.RawMessage
	raw, _ := fields["config_snapshot"].(string)
	if json.Unmarshal([]byte(raw), &outer) != nil || outer == nil {
		return errors.New("本地配置快照无效，未覆盖")
	}
	v := readmodel.SupplierSnapshot{CustomerID: h.CustomerID, OwnershipVersion: h.OwnershipVersion, SupplierID: *h.UpstreamProviderID, RemoteID: h.UpstreamInstanceID, BillingCycle: cycle, ObservedAt: now.Unix(), ObservedStatus: detail.Status, ObservedPower: detail.PowerStatus, PriceState: "unavailable"}
	if selected != nil {
		v.Amount = selected.Amount
		v.PriceSource = "upstream_original"
		v.PriceState = "verified"
		v.VerifiedAt = now.Unix()
	} else if old, ok := readmodel.Decode(h.ConfigSnapshot); ok && readmodel.ValidPrice(old, h.CustomerID, h.OwnershipVersion, *h.UpstreamProviderID, h.UpstreamInstanceID, cycle) {
		v.Amount = old.Amount
		v.PriceSource = old.PriceSource
		v.VerifiedAt = old.VerifiedAt
		v.PriceState = "last_known"
	}
	outer[readmodel.SnapshotKey], _ = json.Marshal(v)
	if cycle != "" {
		outer["billingCycle"], _ = json.Marshal(cycle)
		var display map[string]json.RawMessage
		_ = json.Unmarshal(outer["listDisplay"], &display)
		if display == nil {
			display = map[string]json.RawMessage{}
		}
		display["billingCycle"], _ = json.Marshal(cycle)
		if v.Amount != "" {
			display["renewalAmount"], _ = json.Marshal(v.Amount)
		} else {
			delete(display, "renewalAmount")
		}
		outer["listDisplay"], _ = json.Marshal(display)
	}
	encoded, e := json.Marshal(outer)
	if e != nil {
		return e
	}
	fields["config_snapshot"] = string(encoded)
	return nil
}

// SyncSupplierReadSnapshots only reads existing, bound active resources. It
// cannot buy, renew, power off, terminate or change ownership/lifecycle states.
func (s *Service) SyncSupplierReadSnapshots(ctx context.Context, supplier, run string, report func(int, int, int) error) error {
	var rows []struct {
		ID         int64
		CustomerID int64
	}
	if e := s.db.WithContext(ctx).Table("instance.instances").Select("id,customer_id").Where("upstream_provider_id=? AND deleted_at IS NULL AND status IN (3,4)", supplier).Order("id").Find(&rows).Error; e != nil {
		return e
	}
	total, completed, failed := len(rows), 0, 0
	if e := report(total, completed, failed); e != nil {
		return e
	}
	for _, row := range rows {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		e := workgate.Run(ctx, func(call context.Context) error {
			call, cancel := context.WithTimeout(call, 35*time.Second)
			defer cancel()
			return s.withInstanceReadLock(call, row.ID, func() error { return s.syncSupplierReadItem(call, supplier, run, row.ID, row.CustomerID) })
		})
		if e != nil {
			failed++
		}
		completed++
		if e := report(total, completed, failed); e != nil {
			return e
		}
	}
	return nil
}
func (s *Service) syncSupplierReadItem(ctx context.Context, supplier, run string, id, customer int64) error {
	h, e := s.owned(ctx, customer, id)
	if e != nil {
		return e
	}
	if h.UpstreamProviderID == nil || fmt.Sprint(*h.UpstreamProviderID) != supplier || h.Status != 3 && h.Status != 4 {
		return errors.New("实例资源绑定已变化")
	}
	p, warning := s.providerContext(ctx, h)
	if warning != "" {
		return errors.New("供应商信息通道不可用")
	}
	if _, ok := p.(interface {
		WithInstanceContext(context.Context) contracts.InstanceProvider
	}); !ok {
		return errors.New("供应商尚未支持受控只读同步")
	}
	detail, e := p.GetInstanceDetail(parseHostID(h.UpstreamInstanceID))
	if e != nil || detail == nil || detail.ID != h.UpstreamInstanceID {
		return errors.New("供应商信息未确认，保留本地数据")
	}
	options, quoteErr := s.snapshotRenewalOptions(ctx, h)
	cycle := detail.BillingCycle
	if cycle == "" {
		cycle = listItem(h).BillingCycle
	}
	matched := false
	for _, option := range options {
		if option.Cycle == cycle {
			matched = true
		}
	}
	if !matched && quoteErr == nil {
		quoteErr = errors.New("当前续费周期无有效原价")
	}
	e = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := upstream.VerifySupplierSnapshotLeaseTx(ctx, tx, supplier, run); e != nil {
			return e
		}
		var current dom.Host
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND customer_id=? AND ownership_version=? AND upstream_provider_id=? AND upstream_instance_id=? AND deleted_at IS NULL", id, customer, h.OwnershipVersion, *h.UpstreamProviderID, h.UpstreamInstanceID).Take(&current).Error; e != nil {
			return e
		}
		if !sameSyncReadVersion(*h, current) {
			return errors.New("本地信息已更新，跳过旧结果")
		}
		stamp := time.Now().UTC()
		fields, e := safeSyncFields(&current, detail, stamp)
		if e != nil {
			return e
		}
		if e = applySupplierReadSnapshot(&current, detail, fields, options, quoteErr, stamp); e != nil {
			return e
		}
		return tx.Model(&current).Updates(fields).Error
	})
	if e != nil {
		return e
	}
	if quoteErr != nil {
		return errors.New("续费原价未更新，保留上次有效报价")
	}
	return nil
}
