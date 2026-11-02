package instance

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	dom "ddyidc/internal/instance/domain"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/internal/shared/workgate"
	"ddyidc/internal/upstream/metrics"
	"ddyidc/internal/upstream/provider/contracts"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

//go:embed admin_schema.sql
var adminSchema string

func (s *Service) ensureAdminSchema(ctx context.Context) error {
	for _, sql := range strings.Split(adminSchema, ";") {
		if strings.TrimSpace(sql) != "" {
			if e := s.db.WithContext(ctx).Exec(sql).Error; e != nil {
				return e
			}
		}
	}
	return nil
}

type AdminSyncSelection struct {
	ID               string `json:"id"`
	CustomerID       string `json:"customerId"`
	OwnershipVersion string `json:"ownershipVersion"`
}
type AdminSyncItem struct {
	AdminSyncSelection
	State   string `json:"state"`
	Message string `json:"message"`
}
type AdminSyncInput struct {
	RequestID string               `json:"requestId"`
	Items     []AdminSyncSelection `json:"items"`
}
type AdminSyncTask struct {
	ID         int64     `gorm:"primaryKey" json:"-"`
	AdminID    int64     `json:"-"`
	RequestID  string    `json:"-"`
	OperatorIP string    `json:"-"`
	State      string    `json:"state"`
	Items      string    `gorm:"type:json" json:"-"`
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

func (AdminSyncTask) TableName() string { return "instance.admin_sync_tasks" }

type AdminSyncTaskView struct {
	ID        string          `json:"id"`
	State     string          `json:"state"`
	Items     []AdminSyncItem `json:"items"`
	CreatedAt time.Time       `json:"createdAt"`
	UpdatedAt time.Time       `json:"updatedAt"`
}

func syncTaskView(t AdminSyncTask) AdminSyncTaskView {
	v := AdminSyncTaskView{ID: strconv.FormatInt(t.ID, 10), State: t.State, Items: []AdminSyncItem{}, CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt}
	_ = json.Unmarshal([]byte(t.Items), &v.Items)
	if v.State == "running" && time.Since(t.UpdatedAt) > 2*time.Minute {
		v.State = "interrupted"
	}
	return v
}

func (s *Service) StartAdminSync(ctx context.Context, a AdminAccess, ip string, in AdminSyncInput) (*AdminSyncTaskView, error) {
	if !a.Sync || a.AdminID <= 0 {
		return nil, ErrForbidden
	}
	if net.ParseIP(ip) == nil || len(in.RequestID) < 16 || len(in.RequestID) > 64 || strings.ContainsAny(in.RequestID, " \t\n") || len(in.Items) == 0 || len(in.Items) > 100 {
		return nil, ErrAdminListInput
	}
	var existing AdminSyncTask
	if e := s.db.WithContext(ctx).Where("admin_id=? AND request_id=?", a.AdminID, in.RequestID).Take(&existing).Error; e == nil {
		v := syncTaskView(existing)
		return &v, nil
	} else if !errors.Is(e, gorm.ErrRecordNotFound) {
		return nil, e
	}
	items := []AdminSyncItem{}
	seen := map[int64]bool{}
	for _, selection := range in.Items {
		id, e := strconv.ParseInt(selection.ID, 10, 64)
		customer, ce := strconv.ParseInt(selection.CustomerID, 10, 64)
		version, ve := strconv.ParseInt(selection.OwnershipVersion, 10, 64)
		if e != nil || ce != nil || ve != nil || id <= 0 || customer <= 0 || version < 0 {
			return nil, ErrAdminListInput
		}
		if !a.Allows(customer) {
			return nil, ErrForbidden
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		var h dom.Host
		if e := s.db.WithContext(ctx).Where("id=? AND customer_id=? AND ownership_version=? AND status IN (3,4)", id, customer, version).Take(&h).Error; e != nil {
			return nil, errors.New("选择中包含已变化归属或非运行/停机/暂停实例，请刷新列表")
		}
		items = append(items, AdminSyncItem{AdminSyncSelection: selection, State: "pending", Message: "等待只读同步"})
	}
	pool, e := s.db.DB()
	if e != nil {
		return nil, e
	}
	conn, e := pool.Conn(ctx)
	if e != nil {
		return nil, e
	}
	var locked int
	if e = conn.QueryRowContext(ctx, "SELECT GET_LOCK('ddyidc:admin-instance-sync',0)").Scan(&locked); e != nil || locked != 1 {
		conn.Close()
		return nil, errors.New("已有只读批量同步任务执行中，请稍后重试")
	}
	release := func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = conn.ExecContext(cleanup, "SELECT RELEASE_LOCK('ddyidc:admin-instance-sync')")
		_ = conn.Close()
	}
	raw, _ := json.Marshal(items)
	now := time.Now().UTC()
	task := AdminSyncTask{ID: snowflake.GenID(), AdminID: a.AdminID, RequestID: in.RequestID, OperatorIP: ip, State: "running", Items: string(raw), CreatedAt: now, UpdatedAt: now}
	if e = s.db.WithContext(ctx).Create(&task).Error; e != nil {
		release()
		return nil, e
	}
	go func() {
		defer release()
		call, cancel := context.WithTimeout(metrics.WithBackground(context.Background()), 50*time.Minute)
		defer cancel()
		for i := range items {
			items[i].State = "running"
			items[i].Message = "正在读取供应商信息"
			if s.persistAdminSync(call, &task, items) != nil {
				return
			}
			e := workgate.Run(call, func(ctx context.Context) error { return s.syncAdminItem(ctx, task, items[i].AdminSyncSelection) })
			if e != nil {
				items[i].State = "failed"
				items[i].Message = adminSyncFailure(e)
				id, _ := strconv.ParseInt(items[i].ID, 10, 64)
				customer, _ := strconv.ParseInt(items[i].CustomerID, 10, 64)
				_ = s.db.WithContext(call).Exec("INSERT INTO instance.admin_instance_audits(id,admin_id,operator_ip,instance_id,customer_id,task_id,action,before_value,after_value,result,created_at) VALUES(?,?,?,?,?,?,'sync','{}',?,'failed',?)", snowflake.GenID(), task.AdminID, task.OperatorIP, id, customer, task.ID, mustAdminJSON(map[string]string{"message": items[i].Message}), time.Now().UTC()).Error
			} else {
				items[i].State = "success"
				items[i].Message = "只读信息同步完成"
			}
			if s.persistAdminSync(call, &task, items) != nil {
				return
			}
			if call.Err() != nil {
				for j := i + 1; j < len(items); j++ {
					items[j].State = "failed"
					items[j].Message = "任务超时，未请求供应商"
				}
				break
			}
		}
		task.State = "completed"
		for _, it := range items {
			if it.State != "success" {
				task.State = "completed_with_errors"
				break
			}
		}
		finish, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = s.persistAdminSync(finish, &task, items)
	}()
	view := syncTaskView(task)
	return &view, nil
}
func (s *Service) persistAdminSync(ctx context.Context, t *AdminSyncTask, items []AdminSyncItem) error {
	raw, _ := json.Marshal(items)
	t.Items = string(raw)
	t.UpdatedAt = time.Now().UTC()
	return s.db.WithContext(ctx).Model(t).Updates(map[string]any{"items": t.Items, "state": t.State, "updated_at": t.UpdatedAt}).Error
}
func (s *Service) AdminSyncResult(ctx context.Context, a AdminAccess, id int64) (*AdminSyncTaskView, error) {
	if !a.Sync {
		return nil, ErrForbidden
	}
	var t AdminSyncTask
	if e := s.db.WithContext(ctx).Where("id=? AND admin_id=?", id, a.AdminID).Take(&t).Error; e != nil {
		return nil, ErrNotFound
	}
	v := syncTaskView(t)
	for _, item := range v.Items {
		customer, _ := strconv.ParseInt(item.CustomerID, 10, 64)
		if !a.Allows(customer) {
			return nil, ErrForbidden
		}
	}
	return &v, nil
}

var adminConfigAllowed = map[string]bool{"cpu": true, "memory": true, "system_disk": true, "data_disk": true, "bandwidth": true, "region": true, "data_center": true, "network": true, "defense": true}

func safeSyncFields(h *dom.Host, detail *contracts.InstanceDetail, now time.Time) (map[string]any, error) {
	if detail == nil || detail.ID != h.UpstreamInstanceID {
		return nil, errors.New("供应商资源身份不匹配")
	}
	fields := map[string]any{"last_synced_at": now, "updated_at": now}
	if detail.ExpiresAt != nil && detail.ExpiresAt.Year() >= 1970 && detail.ExpiresAt.Year() <= 2100 {
		fields["expires_at"] = *detail.ExpiresAt
	}
	if net.ParseIP(detail.PrimaryIP) != nil {
		fields["primary_ip"] = detail.PrimaryIP
	}
	if utf8.ValidString(detail.OperatingSystem) && utf8.RuneCountInString(detail.OperatingSystem) <= 128 && detail.OperatingSystem != "" {
		fields["operating_system"] = detail.OperatingSystem
	}
	var snapshot map[string]json.RawMessage
	if json.Unmarshal([]byte(h.ConfigSnapshot), &snapshot) != nil || snapshot == nil {
		return nil, errors.New("本地配置快照无效，未覆盖")
	}
	ips := []contracts.InstanceIP{}
	for _, ip := range detail.IPs {
		if net.ParseIP(ip.Address) != nil {
			ips = append(ips, contracts.InstanceIP{Address: ip.Address})
		}
	}
	if len(ips) > 256 {
		return nil, errors.New("供应商 IP 数量超限")
	}
	if len(ips) > 0 {
		snapshot["syncedIps"], _ = json.Marshal(ips)
	}
	allowed := adminConfigAllowed
	config := []contracts.InstanceConfig{}
	for _, c := range detail.Config {
		if allowed[c.Field] && utf8.ValidString(c.Value) && len(c.Value) <= 1024 {
			config = append(config, contracts.InstanceConfig{Field: c.Field, Value: c.Value})
		}
	}
	if len(config) > 0 {
		snapshot["syncedConfig"], _ = json.Marshal(config)
	}
	// Store safe display facts for the local-only customer list. Preserve the
	// verified retail price and billing cycle; supplier detail prices may be costs.
	local := listItem(h)
	display := listDisplay{Hostname: local.Hostname, Port: local.Port, OperatingSystem: local.OperatingSystem, BillingCycle: local.BillingCycle, RenewalAmount: local.RenewalAmount}
	if utf8.ValidString(detail.Hostname) && len(detail.Hostname) <= 255 && detail.Hostname != "" {
		display.Hostname = detail.Hostname
	}
	if port, err := strconv.Atoi(detail.Port); err == nil && port > 0 && port <= 65535 {
		display.Port = strconv.Itoa(port)
	}
	if os, ok := fields["operating_system"].(string); ok {
		display.OperatingSystem = os
	}
	snapshot["listDisplay"], _ = json.Marshal(display)
	raw, _ := json.Marshal(snapshot)
	fields["config_snapshot"] = string(raw)
	// Lifecycle, power, ownership, bindings, remarks, auto-renew and pricing are
	// intentionally absent. Reading a remote state is not a state-machine action.
	return fields, nil
}
func (s *Service) syncAdminItem(ctx context.Context, t AdminSyncTask, item AdminSyncSelection) error {
	id, e := strconv.ParseInt(item.ID, 10, 64)
	if e != nil {
		return e
	}
	return s.withInstanceReadLock(ctx, id, func() error { return s.syncAdminItemLocked(ctx, t, item) })
}
func (s *Service) syncAdminItemLocked(ctx context.Context, t AdminSyncTask, item AdminSyncSelection) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	a, e := s.AdminAccess(ctx, t.AdminID)
	if e != nil || !a.Sync {
		return ErrForbidden
	}
	id, _ := strconv.ParseInt(item.ID, 10, 64)
	customer, _ := strconv.ParseInt(item.CustomerID, 10, 64)
	version, _ := strconv.ParseInt(item.OwnershipVersion, 10, 64)
	if !a.Allows(customer) {
		return ErrForbidden
	}
	h, e := s.owned(ctx, customer, id)
	if e != nil || h.OwnershipVersion != version {
		return errors.New("归属已变化，未请求供应商")
	}
	remote, e := strconv.ParseInt(h.UpstreamInstanceID, 10, 64)
	if e != nil || remote <= 0 {
		return errors.New("实例未绑定有效供应商资源")
	}
	p, w := s.provider(h)
	if w != "" {
		return errors.New("供应商通道未配置")
	}
	scoped, ok := p.(interface {
		WithInstanceContext(context.Context) contracts.InstanceProvider
	})
	if !ok {
		return errors.New("供应商通道尚未支持受控只读同步")
	}
	p = scoped.WithInstanceContext(ctx)
	detail, e := p.GetInstanceDetail(remote)
	if e != nil {
		return errors.New("供应商信息读取失败或超时，未覆盖本地信息")
	}
	if detail == nil || detail.ID != h.UpstreamInstanceID {
		return errors.New("供应商资源身份不匹配，未覆盖")
	}
	options, quoteErr := s.snapshotRenewalOptions(ctx, h)
	a, e = s.AdminAccess(ctx, t.AdminID)
	if e != nil || !a.Sync || !a.Allows(customer) {
		return ErrForbidden
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current dom.Host
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND customer_id=? AND ownership_version=? AND upstream_provider_id <=> ? AND upstream_instance_id=? AND status IN (3,4)", id, customer, version, h.UpstreamProviderID, h.UpstreamInstanceID).Take(&current).Error; e != nil {
			return errors.New("实例归属或资源绑定已变化，未覆盖")
		}
		if !sameSyncReadVersion(*h, current) {
			return errors.New("读取期间实例已更新，请重新同步")
		}
		now := time.Now().UTC()
		fields, e := safeSyncFields(&current, detail, now)
		if e != nil {
			return e
		}
		s.cacheSupplierCredentials(fields, detail)
		// Finance refreshes the whole host record on a manual pull. Mirror that:
		// persist the supplier lifecycle and power state alongside expiry/IP/config
		// so one click leaves the local row consistent with the supplier.
		status := statusCode(detail.PowerStatus, current.Status)
		if lifecycle := unavailableLifecycle(detail, current.UpstreamInstanceID); lifecycle != "" {
			fields["power_status"] = lifecycle
			status = statusCode(lifecycle, current.Status)
		} else if detail.PowerStatus != "" {
			fields["power_status"] = detail.PowerStatus
		}
		fields["status"] = status
		if e := applySupplierReadSnapshot(&current, detail, fields, options, quoteErr, now); e != nil {
			return e
		}
		before := adminSyncAudit(current)
		if e := tx.Model(&current).Updates(fields).Error; e != nil {
			return e
		}
		return appendAdminAudit(tx, t.AdminID, t.OperatorIP, id, customer, t.ID, "sync", before, adminSyncAudit(current))
	})
}
func mustAdminJSON(v any) string { raw, _ := json.Marshal(v); return string(raw) }
func adminSyncFailure(e error) string {
	if errors.Is(e, ErrForbidden) {
		return "权限或客户范围已变化，未覆盖"
	}
	if errors.Is(e, workgate.ErrPaused) {
		return "维护期间未执行同步，请稍后重试"
	}
	if errors.Is(e, context.DeadlineExceeded) || errors.Is(e, context.Canceled) {
		return "任务超时或中断，未继续请求"
	}
	for _, safe := range []string{"归属已变化，未请求供应商", "实例未绑定有效供应商资源", "供应商通道未配置", "供应商通道尚未支持受控只读同步", "供应商信息读取失败或超时，未覆盖本地信息", "供应商资源身份不匹配，未覆盖", "实例归属或资源绑定已变化，未覆盖", "读取期间实例已更新，请重新同步", "本地配置快照无效，未覆盖", "供应商 IP 数量超限", "供应商资源身份不匹配"} {
		if e.Error() == safe {
			return safe
		}
	}
	return "本地同步或审计未完成，请刷新后检查结果"
}
func adminSyncAudit(h dom.Host) map[string]any {
	var snapshot struct {
		IPs    []contracts.InstanceIP `json:"syncedIps"`
		Config []AdminConfigValue     `json:"syncedConfig"`
	}
	_ = json.Unmarshal([]byte(h.ConfigSnapshot), &snapshot)
	ips := []string{}
	for _, ip := range snapshot.IPs {
		if net.ParseIP(ip.Address) != nil {
			ips = append(ips, ip.Address)
		}
	}
	config := []AdminConfigValue{}
	for _, c := range snapshot.Config {
		if adminConfigAllowed[c.Field] && utf8.ValidString(c.Value) && len(c.Value) <= 1024 {
			config = append(config, c)
		}
	}
	return map[string]any{"primaryIp": h.PrimaryIP, "expiresAt": h.ExpiresAt, "operatingSystem": h.OperatingSystem, "ips": ips, "config": config, "lastSyncedAt": h.LastSyncedAt}
}
func appendAdminAudit(tx *gorm.DB, admin int64, ip string, id, customer, task int64, action string, before, after any) error {
	b, _ := json.Marshal(before)
	a, _ := json.Marshal(after)
	return tx.Exec("INSERT INTO instance.admin_instance_audits(id,admin_id,operator_ip,instance_id,customer_id,task_id,action,before_value,after_value,result,created_at) VALUES(?,?,?,?,?,?,?,?,?,'success',?)", snowflake.GenID(), admin, ip, id, customer, task, action, string(b), string(a), time.Now().UTC()).Error
}

func (s *Service) AdminExportRows(ctx context.Context, a AdminAccess, in AdminListInput) ([]AdminInstanceRow, error) {
	if !a.Export {
		return nil, ErrForbidden
	}
	in, e := normalizeAdminInput(in)
	if e != nil {
		return nil, e
	}
	rows := []AdminInstanceRow{}
	e = adminView(adminBase(s.db.WithContext(ctx), a, in), in.View, time.Now().UTC()).Select(adminProjection).Order(adminSorts[in.Sort] + " " + in.Direction + ",i.id " + in.Direction).Limit(10001).Scan(&rows).Error
	if e != nil {
		return nil, e
	}
	if len(rows) > 10000 {
		return nil, fmt.Errorf("%w：一次最多导出 10000 台，请缩小筛选范围", ErrAdminListInput)
	}
	for i := range rows {
		decorateAdminRow(&rows[i], a.Contact)
	}
	return rows, nil
}
