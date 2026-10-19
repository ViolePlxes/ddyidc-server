package service

import (
	"context"
	"ddyidc/internal/shared/distsync"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/internal/shared/workgate"
	"ddyidc/internal/upstream/metrics"
	"ddyidc/internal/upstream/model"
	"encoding/json"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"time"
)

// One bounded, latest-state config row per supplier. No credentials/history are
// copied, and no DDL or extra runtime database grants are needed.
type supplierSnapshotState struct {
	model.SupplierSnapshotInfo
	RunID      string `json:"runId,omitempty"`
	LeaseUntil int64  `json:"leaseUntil,omitempty"`
}
type SupplierSnapshotWorker interface {
	SyncSupplierReadSnapshots(context.Context, string, string, func(int, int, int) error) error
}

func (s *Store) SetSupplierSnapshotWorker(w SupplierSnapshotWorker) { s.snapshotWorker = w }
func snapshotKey(id string) string                                  { return "upstream.snapshot." + id }
func defaultSnapshotState() supplierSnapshotState {
	return supplierSnapshotState{SupplierSnapshotInfo: model.SupplierSnapshotInfo{Enabled: true, IntervalMinutes: 120, State: "never"}}
}
func decodeSnapshotState(raw string) (supplierSnapshotState, error) {
	v := defaultSnapshotState()
	if raw != "" {
		if e := json.Unmarshal([]byte(raw), &v); e != nil {
			return v, errors.New("同步设置格式异常")
		}
	}
	if v.IntervalMinutes < 15 || v.IntervalMinutes > 1440 {
		return v, errors.New("同步间隔需为15至1440分钟")
	}
	return v, nil
}
func snapshotView(v supplierSnapshotState) model.SupplierSnapshotInfo {
	out := v.SupplierSnapshotInfo
	if out.State == "running" && v.LeaseUntil <= time.Now().Unix() {
		out.State = "interrupted"
		out.Message = "上次同步中断，等待重新执行"
	}
	return out
}
func snapshotStateTx(tx *gorm.DB, id string) (configRow, supplierSnapshotState, error) {
	initial, _ := json.Marshal(defaultSnapshotState())
	row := configRow{ID: snowflake.GenID(), Key: snapshotKey(id), Value: string(initial), Description: "供应商本地信息同步设置与最近结果"}
	if e := tx.Table("system.system_configs").Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "key"}}, DoNothing: true}).Create(&row).Error; e != nil {
		return row, supplierSnapshotState{}, e
	}
	row.ID = 0
	if e := tx.Table("system.system_configs").Clauses(clause.Locking{Strength: "UPDATE"}).Where("`key`=?", snapshotKey(id)).Take(&row).Error; e != nil {
		return row, supplierSnapshotState{}, e
	}
	state, e := decodeSnapshotState(row.Value)
	return row, state, e
}
func saveSnapshotState(tx *gorm.DB, row configRow, v supplierSnapshotState) error {
	raw, e := json.Marshal(v)
	if e != nil {
		return e
	}
	return tx.Table("system.system_configs").Where("id=? AND `key`=?", row.ID, row.Key).Update("value", string(raw)).Error
}
func (s *Store) SupplierSnapshotState(ctx context.Context, id string) (model.SupplierSnapshotInfo, error) {
	if _, e := s.GetSupplier(id); e != nil {
		return model.SupplierSnapshotInfo{}, e
	}
	if s.isMemory() {
		return snapshotView(defaultSnapshotState()), nil
	}
	var row configRow
	e := s.db.WithContext(ctx).Table("system.system_configs").Where("`key`=?", snapshotKey(id)).Take(&row).Error
	if errors.Is(e, gorm.ErrRecordNotFound) {
		return snapshotView(defaultSnapshotState()), nil
	}
	if e != nil {
		return model.SupplierSnapshotInfo{}, e
	}
	v, e := decodeSnapshotState(row.Value)
	return snapshotView(v), e
}
func (s *Store) ConfigureSupplierSnapshot(ctx context.Context, id string, enabled bool, minutes int) (model.SupplierSnapshotInfo, error) {
	if minutes < 15 || minutes > 1440 {
		return model.SupplierSnapshotInfo{}, errors.New("同步间隔需为15至1440分钟")
	}
	if _, e := s.GetSupplier(id); e != nil {
		return model.SupplierSnapshotInfo{}, e
	}
	if s.isMemory() {
		return model.SupplierSnapshotInfo{}, errors.New("同步设置需要数据库")
	}
	var out model.SupplierSnapshotInfo
	e := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		row, v, e := snapshotStateTx(tx, id)
		if e != nil {
			return e
		}
		v.Enabled = enabled
		v.IntervalMinutes = minutes
		if v.State != "running" {
			v.NextRunAt = 0
		}
		if e = saveSnapshotState(tx, row, v); e != nil {
			return e
		}
		out = snapshotView(v)
		return nil
	})
	return out, e
}
func (s *Store) attachSupplierSnapshots(rows []model.Supplier) {
	keys := []string{}
	for i := range rows {
		v := snapshotView(defaultSnapshotState())
		rows[i].SnapshotSync = &v
		keys = append(keys, snapshotKey(rows[i].ID))
	}
	if len(keys) == 0 || s.isMemory() {
		return
	}
	var stored []configRow
	if e := s.db.Table("system.system_configs").Where("`key` IN ?", keys).Find(&stored).Error; e != nil {
		return
	}
	byKey := map[string]string{}
	for _, r := range stored {
		byKey[r.Key] = r.Value
	}
	for i := range rows {
		if raw := byKey[snapshotKey(rows[i].ID)]; raw != "" {
			v, e := decodeSnapshotState(raw)
			info := snapshotView(v)
			if e != nil {
				info.State = "failed"
				info.Message = "同步设置格式异常"
			}
			rows[i].SnapshotSync = &info
			rows[i].LastSyncedAt = info.LastSuccessAt
		}
	}
}
func (s *Store) StartSupplierSnapshot(ctx context.Context, id string, manual bool) (model.SupplierSnapshotInfo, error) {
	if s.isMemory() || s.snapshotWorker == nil {
		return model.SupplierSnapshotInfo{}, errors.New("信息同步服务尚未配置")
	}
	supplier, e := s.GetSupplier(id)
	if e != nil {
		return model.SupplierSnapshotInfo{}, e
	}
	if supplier.Status != 1 {
		return model.SupplierSnapshotInfo{}, errors.New("供应商已停用")
	}
	select {
	case s.snapshotSlots <- struct{}{}:
	default:
		info, e := s.SupplierSnapshotState(ctx, id)
		if e != nil {
			return info, e
		}
		if info.State == "running" {
			return info, nil
		}
		return info, errors.New("同步队列繁忙，请稍后重试")
	}
	started := false
	defer func() {
		if !started {
			<-s.snapshotSlots
		}
	}()
	var claimed supplierSnapshotState
	e = workgate.Run(ctx, func(call context.Context) error {
		return s.db.WithContext(call).Transaction(func(tx *gorm.DB) error {
			row, v, e := snapshotStateTx(tx, id)
			if e != nil {
				return e
			}
			now := time.Now().Unix()
			if v.State == "running" && v.LeaseUntil > now {
				claimed = v
				return nil
			}
			if !manual && (!v.Enabled || v.NextRunAt > now) {
				claimed = v
				return nil
			}
			v.State = "running"
			v.LastAttemptAt = now
			v.RunID = fmt.Sprint(snowflake.GenID())
			v.LeaseUntil = now + 90
			v.Total = 0
			v.Completed = 0
			v.Failed = 0
			v.Message = "正在同步已关联产品信息"
			if e = saveSnapshotState(tx, row, v); e != nil {
				return e
			}
			claimed = v
			started = true
			return nil
		})
	})
	if e != nil {
		started = false
		return model.SupplierSnapshotInfo{}, e
	}
	if started {
		parent := metrics.WithBackground(ctx)
		if manual {
			parent = metrics.WithBackground(context.Background())
		}
		go s.runSupplierSnapshot(parent, id, claimed.RunID)
	}
	return snapshotView(claimed), nil
}

// The row lock fences publication after a lease has expired or been replaced.
func VerifySupplierSnapshotLeaseTx(ctx context.Context, tx *gorm.DB, id, run string) error {
	var row configRow
	if e := tx.WithContext(ctx).Table("system.system_configs").Clauses(clause.Locking{Strength: "SHARE"}).Where("`key`=?", snapshotKey(id)).Take(&row).Error; e != nil {
		return e
	}
	v, e := decodeSnapshotState(row.Value)
	if e != nil {
		return e
	}
	if v.RunID != run || v.State != "running" || v.LeaseUntil <= time.Now().Unix() {
		return errors.New("同步任务租约已失效")
	}
	return nil
}
func (s *Store) snapshotProgress(ctx context.Context, id, run string, total, completed, failed int) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		row, v, e := snapshotStateTx(tx, id)
		if e != nil {
			return e
		}
		if v.RunID != run || v.State != "running" || v.LeaseUntil <= time.Now().Unix() {
			return errors.New("同步任务已被接管")
		}
		if total >= 0 {
			if failed < 0 || completed < failed || total < completed {
				return errors.New("同步进度无效")
			}
			v.Total = total
			v.Completed = completed
			v.Failed = failed
		}
		v.LeaseUntil = time.Now().Unix() + 90
		return saveSnapshotState(tx, row, v)
	})
}
func (s *Store) runSupplierSnapshot(parent context.Context, id, run string) {
	defer func() { <-s.snapshotSlots }()
	ctx, cancel := context.WithTimeout(parent, 45*time.Minute)
	defer cancel()
	stopped := make(chan struct{})
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		tick := time.NewTicker(20 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-stopped:
				return
			case <-tick.C:
				if s.snapshotProgress(ctx, id, run, -1, 0, 0) != nil {
					cancel()
					return
				}
			}
		}
	}()
	err := s.snapshotWorker.SyncSupplierReadSnapshots(ctx, id, run, func(t, c, f int) error { return s.snapshotProgress(ctx, id, run, t, c, f) })
	close(stopped)
	<-heartbeatDone
	final, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	_ = s.db.WithContext(final).Transaction(func(tx *gorm.DB) error {
		row, v, e := snapshotStateTx(tx, id)
		if e != nil {
			return e
		}
		if v.RunID != run || v.State != "running" {
			return nil
		}
		now := time.Now().Unix()
		expired := v.LeaseUntil <= now
		v.LeaseUntil = 0
		v.RunID = ""
		v.NextRunAt = now + int64(v.IntervalMinutes*60)
		if err == nil && ctx.Err() == nil && !expired && v.Failed == 0 && v.Completed == v.Total {
			v.State = "success"
			v.LastSuccessAt = now
			v.Message = "已关联产品信息同步完成"
		} else {
			v.State = "partial"
			v.Message = "部分信息未同步，保留上次有效数据"
			if v.Failed == 0 {
				v.State = "failed"
			}
			if ctx.Err() != nil || expired {
				v.State = "interrupted"
				v.Message = "同步已中断，保留上次有效数据"
			}
			retry := now + 15*60
			if v.NextRunAt > retry {
				v.NextRunAt = retry
			}
		}
		return saveSnapshotState(tx, row, v)
	})
}
func (s *Store) StartSupplierSnapshotScheduler(ctx context.Context) {
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	scan := func() {
		if !distsync.IsLeader(ctx) {
			return
		}
		for _, supplier := range s.ListSuppliers() {
			if ctx.Err() != nil || !distsync.IsLeader(ctx) {
				return
			}
			if supplier.Status != 1 {
				continue
			}
			_, _ = s.StartSupplierSnapshot(ctx, supplier.ID, false)
		}
	}
	scan()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			scan()
		}
	}
}
