package cdnsell

import (
	"context"
	"ddyidc/internal/cdnsell/provider"
	"ddyidc/internal/shared/snowflake"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"time"
)

const usageWindowsTable = "system.cdnsell_usage_windows"
const usageAllocationsTable = "system.cdnsell_usage_allocations"

type usageWindow struct {
	UnitPrice                    Yuan
	ID, UserID, DomainID         int64
	WindowStart, WindowEnd       time.Time
	ConfirmedBytes, OverageBytes int64
}
type usageAllocation struct{ WindowID, BatchID, ConsumedBytes int64 }

// Customer row serializes every domain's metering against the same prepaid pool.
// Corrections reverse only allocations from this window, never another user's usage.
func (s *Service) ApplyUsage(ctx context.Context, userID, domainID int64, p provider.UsagePoint) error {
	if p.Bytes < 0 || userID <= 0 || domainID <= 0 || !p.End.Equal(p.Start.Add(5*time.Minute)) || !p.Start.Equal(p.Start.Truncate(5*time.Minute)) {
		return errors.New("用量窗口或字节数无效")
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var customer struct{ ID int64 }
		if e := tx.Table("customer.customers").Select("id").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND deleted_at IS NULL", userID).Take(&customer).Error; e != nil {
			return e
		}
		var domain DomainRecord
		if e := tx.Table(domainsTable).Where("id=? AND user_id=?", domainID, userID).Take(&domain).Error; e != nil {
			return ErrForbidden
		}
		var window usageWindow
		e := tx.Table(usageWindowsTable).Where("domain_id=? AND window_start=?", domainID, p.Start).Take(&window).Error
		if errors.Is(e, gorm.ErrRecordNotFound) {
			var old int64
			if e = tx.Table(meterTable).Where("domain_id=? AND window_start=?", domainID, p.Start).Count(&old).Error; e != nil {
				return e
			}
			if old > 0 {
				return errors.New("历史用量缺少批次分配证据，需人工核对，未再次扣量")
			}
			var rate struct{ OveragePrice Yuan }
			if e = tx.Table(settingsTable).Select("overage_price").Where("id=1").Take(&rate).Error; e != nil {
				return e
			}
			if rate.OveragePrice < 0 {
				return errors.New("超额单价无效")
			}
			window = usageWindow{UnitPrice: rate.OveragePrice, ID: snowflake.GenID(), UserID: userID, DomainID: domainID, WindowStart: p.Start, WindowEnd: p.End}
			if e = tx.Table(usageWindowsTable).Create(&window).Error; e != nil {
				return e
			}
		} else if e != nil {
			return e
		}
		if window.UserID != userID || !window.WindowEnd.Equal(p.End) {
			return errors.New("用量窗口归属不一致")
		}
		delta := p.Bytes - window.ConfirmedBytes
		if delta == 0 {
			return nil
		}
		if delta > 0 {
			var batches []Batch
			if e = tx.Table(batchesTable).Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id=? AND status IN ('running','expired') AND remaining_bytes>0 AND effective_time<=? AND expire_time>=?", userID, p.Start, p.End).Order("expire_time,id").Find(&batches).Error; e != nil {
				return e
			}
			left := delta
			for _, b := range batches {
				take := b.RemainingBytes
				if take > left {
					take = left
				}
				if take == 0 {
					continue
				}
				b.RemainingBytes -= take
				b.ConsumedBytes += take
				status := b.Status
				if b.RemainingBytes == 0 {
					status = "exhausted"
				}
				if e = tx.Table(batchesTable).Where("id=?", b.ID).Updates(map[string]any{"remaining_bytes": b.RemainingBytes, "consumed_bytes": b.ConsumedBytes, "status": status}).Error; e != nil {
					return e
				}
				if e = tx.Exec("INSERT INTO "+usageAllocationsTable+"(window_id,batch_id,consumed_bytes) VALUES(?,?,?) ON DUPLICATE KEY UPDATE consumed_bytes=consumed_bytes+VALUES(consumed_bytes)", window.ID, b.ID, take).Error; e != nil {
					return e
				}
				left -= take
				if left == 0 {
					break
				}
			}
			window.OverageBytes += left
		} else {
			left := -delta
			unbilled := window.OverageBytes
			if unbilled > left {
				unbilled = left
			}
			window.OverageBytes -= unbilled
			left -= unbilled
			var allocations []usageAllocation
			if e = tx.Table(usageAllocationsTable).Where("window_id=?", window.ID).Order("batch_id DESC").Find(&allocations).Error; e != nil {
				return e
			}
			for _, a := range allocations {
				take := a.ConsumedBytes
				if take > left {
					take = left
				}
				if take == 0 {
					continue
				}
				var b Batch
				if e = tx.Table(batchesTable).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND user_id=?", a.BatchID, userID).Take(&b).Error; e != nil {
					return e
				}
				if b.ConsumedBytes < take || b.RemainingBytes+take > b.OriginalBytes {
					return errors.New("批次用量不一致，停止自动修正")
				}
				b.ConsumedBytes -= take
				b.RemainingBytes += take
				status := "running"
				if !b.ExpireTime.After(s.now()) {
					status = "expired"
				}
				if e = tx.Table(batchesTable).Where("id=?", b.ID).Updates(map[string]any{"remaining_bytes": b.RemainingBytes, "consumed_bytes": b.ConsumedBytes, "status": status}).Error; e != nil {
					return e
				}
				if e = tx.Table(usageAllocationsTable).Where("window_id=? AND batch_id=?", window.ID, b.ID).Update("consumed_bytes", a.ConsumedBytes-take).Error; e != nil {
					return e
				}
				left -= take
				if left == 0 {
					break
				}
			}
			if left != 0 {
				return fmt.Errorf("用量修正缺少%d字节分配证据", left)
			}
		}
		if e = tx.Table(meterTable).Where("domain_id=? AND window_start=? AND status='confirmed'", domainID, p.Start).Updates(map[string]any{"status": "revised", "revised_bytes": p.Bytes}).Error; e != nil {
			return e
		}
		if e = tx.Exec("INSERT INTO "+meterTable+"(id,user_id,domain_id,window_start,window_end,confirmed_bytes,revised_bytes,status,source) VALUES(?,?,?,?,?,?,0,'confirmed','usage_sync')", snowflake.GenID(), userID, domainID, p.Start, p.End, p.Bytes).Error; e != nil {
			return e
		}
		return tx.Table(usageWindowsTable).Where("id=?", window.ID).Updates(map[string]any{"confirmed_bytes": p.Bytes, "overage_bytes": window.OverageBytes}).Error
	})
	if err == nil {
		s.warnCustomer(ctx, userID)
	}
	return err
}
