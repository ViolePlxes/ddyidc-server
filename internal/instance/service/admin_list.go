package instance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"gorm.io/gorm"
)

var ErrAdminListInput = errors.New("产品列表参数无效")

// A global permission explicitly grants all customers. Optional per-admin scope
// in system_configs narrows it to customer IDs; malformed scopes fail closed.
type AdminAccess struct {
	AdminID               int64
	CustomerIDs           []int64
	Contact, Export, Sync, Edit bool
}

func (s *Service) AdminAccess(ctx context.Context, adminID int64) (AdminAccess, error) {
	a := AdminAccess{AdminID: adminID}
	if s.db == nil || adminID <= 0 {
		return a, ErrForbidden
	}
	var admin struct {
		IsSuper bool
		Status  uint8
	}
	if e := s.db.WithContext(ctx).Table("system.admins").Select("is_super,status").Where("id=?", adminID).Take(&admin).Error; e != nil || admin.Status != 1 {
		return a, ErrForbidden
	}
	allowed := map[string]bool{}
	if admin.IsSuper {
		for _, p := range []string{"list", "export", "sync", "contact", "edit"} {
			allowed[p] = true
		}
	} else {
		var codes []string
		if e := s.db.WithContext(ctx).Table("system.permissions AS p").Joins("JOIN system.role_permissions rp ON rp.permission_id=p.id").Joins("JOIN system.roles r ON r.id=rp.role_id AND r.status=1").Joins("JOIN system.admin_roles ar ON ar.role_id=r.id").Where("ar.admin_id=? AND p.module='instance'", adminID).Distinct().Pluck("p.code", &codes).Error; e != nil {
			return a, e
		}
		for _, p := range codes {
			allowed[strings.TrimPrefix(p, "instance:")] = true
		}
	}
	if !allowed["list"] {
		return a, ErrForbidden
	}
	a.Export, a.Sync, a.Contact, a.Edit = allowed["export"], allowed["sync"], allowed["contact"], allowed["edit"]
	var scope struct{ Value string }
	e := s.db.WithContext(ctx).Table("system.system_configs").Select("value").Where("`key`=?", fmt.Sprintf("instance.scope.admin.%d", adminID)).Take(&scope).Error
	if e == nil {
		var ids []string
		if json.Unmarshal([]byte(scope.Value), &ids) != nil || len(ids) == 0 || len(ids) > 10000 {
			return a, ErrForbidden
		}
		for _, id := range ids {
			v, e := strconv.ParseInt(id, 10, 64)
			if e != nil || v <= 0 {
				return a, ErrForbidden
			}
			a.CustomerIDs = append(a.CustomerIDs, v)
		}
	} else if !errors.Is(e, gorm.ErrRecordNotFound) {
		return a, e
	}
	return a, nil
}
func (a AdminAccess) Allows(customer int64) bool {
	if len(a.CustomerIDs) == 0 {
		return customer > 0
	}
	for _, id := range a.CustomerIDs {
		if id == customer {
			return true
		}
	}
	return false
}

type AdminListInput struct {
	Page          int    `json:"page"`
	PageSize      int    `json:"pageSize"`
	Search        string `json:"search"`
	View          string `json:"view"`
	CustomerID    string `json:"customerId"`
	ProductID     string `json:"productId"`
	SupplierID    string `json:"supplierId"`
	Status        string `json:"status"`
	Cycle         string `json:"cycle"`
	AutoRenew     string `json:"autoRenew"`
	ActivatedFrom string `json:"activatedFrom"`
	ActivatedTo   string `json:"activatedTo"`
	CreatedFrom   string `json:"createdFrom"`
	CreatedTo     string `json:"createdTo"`
	ExpiresFrom   string `json:"expiresFrom"`
	ExpiresTo     string `json:"expiresTo"`
	MinAmount     string `json:"minAmount"`
	MaxAmount     string `json:"maxAmount"`
	Sort          string `json:"sort"`
	Direction     string `json:"direction"`
}
type AdminInstanceRow struct {
	ID                 string             `json:"id"`
	CustomerID         string             `json:"customerId"`
	ProductID          string             `json:"productId"`
	SupplierID         string             `json:"supplierId"`
	OrderID            string             `json:"orderId"`
	InstanceNo         string             `json:"instanceNo"`
	Hostname           string             `json:"hostname"`
	ProductName        string             `json:"productName"`
	Username           string             `json:"username"`
	RealName           string             `json:"realName"`
	RealNameRecordID   string             `json:"realNameRecordId"`
	RenewalSyncedAt    int64              `json:"renewalSyncedAt"`
	RenewalStale       bool               `json:"renewalStale" gorm:"-"`
	RenewalPriceState  string             `json:"-"`
	Phone              string             `json:"phone"`
	Email              string             `json:"email"`
	SupplierName       string             `json:"supplierName"`
	OrderNo            string             `json:"orderNo"`
	Remark             string             `json:"remark"`
	PrimaryIP          string             `json:"primaryIp"`
	Port               string             `json:"port"`
	Status             uint8              `json:"status"`
	PowerStatus        string             `json:"powerStatus"`
	Cycle              string             `json:"cycle"`
	RenewalAmount      *string            `json:"renewalAmount"`
	AutoRenew          bool               `json:"autoRenew"`
	UpstreamInstanceID string             `json:"upstreamInstanceId"`
	OwnershipVersion   string             `json:"ownershipVersion"`
	OperatingSystem    string             `json:"operatingSystem"`
	CreatedAt          time.Time          `json:"createdAt"`
	ActivatedAt        *time.Time         `json:"activatedAt"`
	ExpiresAt          *time.Time         `json:"expiresAt"`
	LastSyncedAt       *time.Time         `json:"lastSyncedAt"`
	ConfigJSON         string             `json:"-" gorm:"column:config_json"`
	Config             []AdminConfigValue `json:"config" gorm:"-"`
	IPJSON             string             `json:"-" gorm:"column:ip_json"`
	IPs                []string           `json:"ips" gorm:"-"`
	ErrorCount         int64              `json:"errorCount"`
	CustomerMissing    bool               `json:"customerMissing"`
}
type AdminConfigValue struct {
	Field string `json:"field"`
	Value string `json:"value"`
}
type AdminListResult struct {
	Items             []AdminInstanceRow `json:"items"`
	Total             int64              `json:"total"`
	Page              int                `json:"page"`
	PageSize          int                `json:"pageSize"`
	Counts            map[string]int64   `json:"counts"`
	PageRenewalSum    string             `json:"pageRenewalSum"`
	TotalRenewalSum   string             `json:"totalRenewalSum"`
	MissingPriceCount int64              `json:"missingPriceCount"`
	SampledAt         time.Time          `json:"sampledAt"`
	Access            AdminAccessView    `json:"access"`
}
type AdminAccessView struct {
	Export  bool `json:"export"`
	Sync    bool `json:"sync"`
	Contact bool `json:"contact"`
	Edit    bool `json:"edit"`
}

// Display the same ownership-bound retail quote as the expiring queue. Never
// fall back to legacy renewalAmount/listDisplay (which can contain supplier cost).
const cycleSQL = "COALESCE(NULLIF(JSON_UNQUOTE(JSON_EXTRACT(i.config_snapshot,'$.billingCycle')),''),JSON_UNQUOTE(JSON_EXTRACT(i.config_snapshot,'$.listDisplay.billingCycle')),'')"
const snapshotSQL = "JSON_EXTRACT(i.config_snapshot,'$.supplierReadSnapshot')"
const quoteAmountSQL = "JSON_UNQUOTE(JSON_EXTRACT(" + snapshotSQL + ",'$.amount'))"
const quoteVerifiedSQL = "JSON_UNQUOTE(JSON_EXTRACT(" + snapshotSQL + ",'$.customerId'))=CAST(i.customer_id AS CHAR) AND JSON_UNQUOTE(JSON_EXTRACT(" + snapshotSQL + ",'$.ownershipVersion'))=CAST(i.ownership_version AS CHAR) AND i.upstream_provider_id>0 AND COALESCE(i.upstream_instance_id,'')<>'' AND JSON_UNQUOTE(JSON_EXTRACT(" + snapshotSQL + ",'$.supplierId'))=CAST(i.upstream_provider_id AS CHAR) AND JSON_UNQUOTE(JSON_EXTRACT(" + snapshotSQL + ",'$.remoteId'))=i.upstream_instance_id AND " + cycleSQL + "<>'' AND JSON_UNQUOTE(JSON_EXTRACT(" + snapshotSQL + ",'$.billingCycle'))=" + cycleSQL + " AND JSON_UNQUOTE(JSON_EXTRACT(" + snapshotSQL + ",'$.priceSource'))='upstream_original' AND JSON_UNQUOTE(JSON_EXTRACT(" + snapshotSQL + ",'$.priceState')) IN ('verified','last_known') AND JSON_UNQUOTE(JSON_EXTRACT(" + snapshotSQL + ",'$.verifiedAt')) REGEXP '^[0-9]+$' AND CAST(JSON_UNQUOTE(JSON_EXTRACT(" + snapshotSQL + ",'$.verifiedAt')) AS UNSIGNED)>0"
const renewalSQL = "CASE WHEN (" + quoteVerifiedSQL + ") AND " + quoteAmountSQL + " REGEXP '^[0-9]{1,12}([.][0-9]{1,2})?$' AND CAST(" + quoteAmountSQL + " AS DECIMAL(14,2))>0 THEN CAST(" + quoteAmountSQL + " AS DECIMAL(14,2)) ELSE NULL END"
const customerNameSQL = "CASE WHEN c.username REGEXP '^v10[_-][0-9]+$' THEN CAST(i.customer_id AS CHAR) ELSE COALESCE(NULLIF(c.username,''),CAST(i.customer_id AS CHAR)) END"
const ipSQL = "COALESCE(JSON_EXTRACT(i.config_snapshot,'$.syncedIps'),JSON_EXTRACT(i.config_snapshot,'$.ips'),JSON_ARRAY())"
const runningSQL = "i.status=3 AND COALESCE(i.power_status,'') NOT IN ('suspended','deleted','expired','fault')"
const suspendedSQL = "COALESCE(i.power_status,'')='suspended' AND i.status<>6"
const recurringSQL = "" + cycleSQL + " NOT IN ('','free','onetime','one_time','once') AND i.status<>6 AND i.expires_at IS NOT NULL"
const abnormalSQL = "(i.status=1 OR COALESCE(i.power_status,'')='fault' OR c.id IS NULL OR EXISTS(SELECT 1 FROM instance.instance_events e WHERE e.instance_id=i.id AND e.status=3)) AND i.status<>6"

var adminSorts = map[string]string{"createdAt": "i.created_at", "expiresAt": "i.expires_at", "activatedAt": "i.activated_at", "renewalAmount": renewalSQL, "customer": customerNameSQL, "product": "i.product_name"}

func normalizeAdminInput(in AdminListInput) (AdminListInput, error) {
	if in.Page < 1 {
		in.Page = 1
	}
	if in.Page > 100000 {
		return in, ErrAdminListInput
	}
	if in.PageSize < 1 {
		in.PageSize = 20
	}
	if in.PageSize > 100 {
		return in, ErrAdminListInput
	}
	in.Search = strings.TrimSpace(in.Search)
	if !utf8.ValidString(in.Search) || utf8.RuneCountInString(in.Search) > 128 {
		return in, ErrAdminListInput
	}
	if in.View == "" {
		in.View = "all"
	}
	switch in.View {
	case "all", "running", "suspended", "due", "expired", "abnormal", "terminated":
	default:
		return in, ErrAdminListInput
	}
	for _, id := range []string{in.CustomerID, in.ProductID, in.SupplierID} {
		if id != "" {
			v, e := strconv.ParseInt(id, 10, 64)
			if e != nil || v <= 0 {
				return in, ErrAdminListInput
			}
		}
	}
	if in.Status != "" {
		v, e := strconv.Atoi(in.Status)
		if e != nil || v < 1 || v > 6 {
			return in, ErrAdminListInput
		}
	}
	if len(in.Cycle) > 32 {
		return in, ErrAdminListInput
	}
	if in.AutoRenew != "" && in.AutoRenew != "true" && in.AutoRenew != "false" {
		return in, ErrAdminListInput
	}
	for _, v := range []string{in.ActivatedFrom, in.ActivatedTo, in.CreatedFrom, in.CreatedTo, in.ExpiresFrom, in.ExpiresTo} {
		if v != "" {
			d, e := time.Parse(time.RFC3339, v)
			if e != nil || d.Year() < 1970 || d.Year() > 2100 {
				return in, ErrAdminListInput
			}
		}
	}
	for _, v := range []string{in.MinAmount, in.MaxAmount} {
		if v != "" {
			c, e := parseAdminMoney(v)
			if e != nil || c < 0 {
				return in, ErrAdminListInput
			}
		}
	}
	if in.MinAmount != "" && in.MaxAmount != "" {
		a, _ := parseAdminMoney(in.MinAmount)
		b, _ := parseAdminMoney(in.MaxAmount)
		if a > b {
			return in, ErrAdminListInput
		}
	}
	for _, pair := range [][2]string{{in.ActivatedFrom, in.ActivatedTo}, {in.CreatedFrom, in.CreatedTo}, {in.ExpiresFrom, in.ExpiresTo}} {
		if pair[0] != "" && pair[1] != "" {
			a, _ := time.Parse(time.RFC3339, pair[0])
			b, _ := time.Parse(time.RFC3339, pair[1])
			if a.After(b) {
				return in, ErrAdminListInput
			}
		}
	}
	if in.Sort == "" {
		in.Sort = "createdAt"
	}
	if _, ok := adminSorts[in.Sort]; !ok {
		return in, ErrAdminListInput
	}
	if in.Direction == "" {
		in.Direction = "desc"
	}
	if in.Direction != "asc" && in.Direction != "desc" {
		return in, ErrAdminListInput
	}
	return in, nil
}

func adminBase(db *gorm.DB, a AdminAccess, in AdminListInput) *gorm.DB {
	q := db.Table("instance.instances AS i").Joins("LEFT JOIN customer.customers c ON c.id=i.customer_id").Joins("LEFT JOIN common.suppliers s ON s.id=i.upstream_provider_id").Joins("LEFT JOIN `order`.orders o ON o.id=i.order_id").Joins("LEFT JOIN instance.instance_renewal_settings rs ON rs.instance_id=i.id AND rs.customer_id=i.customer_id").Where("i.deleted_at IS NULL")
	if len(a.CustomerIDs) > 0 {
		q = q.Where("i.customer_id IN ?", a.CustomerIDs)
	}
	for field, value := range map[string]string{"i.customer_id": in.CustomerID, "i.product_id": in.ProductID, "i.upstream_provider_id": in.SupplierID, "i.status": in.Status} {
		if value != "" {
			q = q.Where(field+"=?", value)
		}
	}
	if in.Cycle != "" {
		q = q.Where(cycleSQL+"=?", in.Cycle)
	}
	if in.AutoRenew != "" {
		q = q.Where("COALESCE(rs.enabled,0)=?", in.AutoRenew == "true")
	}
	for _, f := range []struct{ col, op, v string }{{"i.activated_at", ">=", in.ActivatedFrom}, {"i.activated_at", "<=", in.ActivatedTo}, {"i.created_at", ">=", in.CreatedFrom}, {"i.created_at", "<=", in.CreatedTo}, {"i.expires_at", ">=", in.ExpiresFrom}, {"i.expires_at", "<=", in.ExpiresTo}, {renewalSQL, ">=", in.MinAmount}, {renewalSQL, "<=", in.MaxAmount}} {
		if f.v != "" {
			var v any = f.v
			if strings.HasSuffix(f.col, "_at") {
				v, _ = time.Parse(time.RFC3339, f.v)
			}
			q = q.Where(f.col+f.op+"?", v)
		}
	}
	if in.Search != "" {
		kw := in.Search
		like := "%" + strings.NewReplacer("!", "!!", "%", "!%", "_", "!_").Replace(kw) + "%"
		ipExists := "EXISTS(SELECT 1 FROM JSON_TABLE(" + ipSQL + ", '$[*]' COLUMNS(address VARCHAR(45) PATH '$.address' NULL ON ERROR)) jip WHERE jip.address=?)"
		if net.ParseIP(kw) != nil {
			q = q.Where("i.primary_ip=? OR "+ipExists, kw, kw)
		} else if id, e := strconv.ParseInt(kw, 10, 64); e == nil && id > 0 {
			q = q.Where("i.id=? OR i.customer_id=? OR i.instance_no=? OR c.phone=? OR i.upstream_instance_id=?", id, id, kw, kw, kw)
		} else {
			predicate := []string{}
			args := []any{}
			for _, col := range []string{"i.instance_no", "i.product_name", "i.remark", "i.primary_ip", "c.username", "JSON_UNQUOTE(JSON_EXTRACT(i.config_snapshot,'$.hostname'))"} {
				predicate = append(predicate, col+" LIKE ? ESCAPE '!'")
				args = append(args, like)
			}
			// Unprivileged staff may search contact identifiers only as exact values.
			predicate = append(predicate, "c.phone=?", "c.email=?", ipExists)
			args = append(args, kw, kw, kw)
			q = q.Where(strings.Join(predicate, " OR "), args...)
		}
	}
	return q
}
func adminView(q *gorm.DB, view string, now time.Time) *gorm.DB {
	switch view {
	case "running":
		return q.Where(runningSQL)
	case "suspended":
		return q.Where(suspendedSQL)
	case "due":
		return q.Where(recurringSQL).Where("i.expires_at>? AND i.expires_at<=?", now, now.Add(72*time.Hour))
	case "expired":
		return q.Where(recurringSQL).Where("i.expires_at<=?", now)
	case "abnormal":
		return q.Where(abnormalSQL)
	case "terminated":
		return q.Where("i.status=6")
	}
	return q
}

const adminProjection = "CAST(i.id AS CHAR) id,CAST(i.customer_id AS CHAR) customer_id,CAST(i.product_id AS CHAR) product_id,COALESCE(CAST(i.upstream_provider_id AS CHAR),'') supplier_id,CAST(i.order_id AS CHAR) order_id,i.instance_no,COALESCE(JSON_UNQUOTE(JSON_EXTRACT(i.config_snapshot,'$.hostname')),'') hostname,i.product_name," + customerNameSQL + " username,COALESCE((SELECT NULLIF(TRIM(cr.name),'') FROM customer.customer_realname cr WHERE cr.customer_id=c.id AND cr.status=2 ORDER BY cr.updated_at DESC,cr.id DESC LIMIT 1),'') real_name,COALESCE((SELECT CAST(cr.id AS CHAR) FROM customer.customer_realname cr WHERE cr.customer_id=c.id AND cr.status=2 AND NULLIF(TRIM(cr.name),'') IS NOT NULL ORDER BY cr.updated_at DESC,cr.id DESC LIMIT 1),'') real_name_record_id,COALESCE(c.phone,'') phone,COALESCE(c.email,'') email,COALESCE(s.name,'') supplier_name,COALESCE(o.order_no,JSON_UNQUOTE(JSON_EXTRACT(i.config_snapshot,'$.legacyOrderNo')),'') order_no,i.remark,COALESCE(i.primary_ip,'') primary_ip,COALESCE(JSON_UNQUOTE(JSON_EXTRACT(i.config_snapshot,'$.listDisplay.port')),'') port,i.status,COALESCE(i.power_status,'') power_status," + cycleSQL + " cycle,CAST((" + renewalSQL + ") AS CHAR) renewal_amount,CASE WHEN (" + renewalSQL + ") IS NOT NULL THEN CAST(JSON_UNQUOTE(JSON_EXTRACT(" + snapshotSQL + ",'$.verifiedAt')) AS UNSIGNED) ELSE 0 END renewal_synced_at,JSON_UNQUOTE(JSON_EXTRACT(" + snapshotSQL + ",'$.priceState')) renewal_price_state,COALESCE(rs.enabled,0) auto_renew,COALESCE(i.upstream_instance_id,'') upstream_instance_id,CAST(i.ownership_version AS CHAR) ownership_version,COALESCE(i.operating_system,'') operating_system,i.created_at,i.activated_at,i.expires_at,i.last_synced_at,CAST(" + ipSQL + " AS CHAR) ip_json,(SELECT COUNT(*) FROM instance.instance_events e WHERE e.instance_id=i.id AND e.status=3) error_count,(c.id IS NULL) customer_missing"

func (s *Service) AdminList(ctx context.Context, a AdminAccess, in AdminListInput) (*AdminListResult, error) {
	in, e := normalizeAdminInput(in)
	if e != nil {
		return nil, e
	}
	if s.db == nil || a.AdminID <= 0 {
		return nil, ErrForbidden
	}
	now := time.Now().UTC()
	result := &AdminListResult{Items: []AdminInstanceRow{}, Page: in.Page, PageSize: in.PageSize, Counts: map[string]int64{}, SampledAt: now, PageRenewalSum: "0.00", TotalRenewalSum: "0.00", Access: AdminAccessView{a.Export, a.Sync, a.Contact, a.Edit}}
	e = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, v := range []string{"all", "running", "suspended", "due", "expired", "abnormal", "terminated"} {
			var n int64
			if err := adminView(adminBase(tx, a, in), v, now).Count(&n).Error; err != nil {
				return err
			}
			result.Counts[v] = n
		}
		q := func() *gorm.DB { return adminView(adminBase(tx, a, in), in.View, now) }
		if err := q().Count(&result.Total).Error; err != nil {
			return err
		}
		var sum struct {
			Amount  string
			Missing int64
		}
		if err := q().Select("CAST(COALESCE(SUM(" + renewalSQL + "),0) AS CHAR) amount,COALESCE(SUM((" + renewalSQL + ") IS NULL),0) missing").Scan(&sum).Error; err != nil {
			return err
		}
		result.TotalRenewalSum = sum.Amount
		result.MissingPriceCount = sum.Missing
		if err := q().Select(adminProjection + ",CAST(COALESCE(JSON_EXTRACT(i.config_snapshot,'$.syncedConfig'),JSON_ARRAY()) AS CHAR) config_json").Order(adminSorts[in.Sort] + " " + in.Direction + ",i.id " + in.Direction).Offset((in.Page - 1) * in.PageSize).Limit(in.PageSize).Scan(&result.Items).Error; err != nil {
			return err
		}
		var pageCents int64
		for i := range result.Items {
			r := &result.Items[i]
			decorateAdminRow(r, a.Contact)
			if r.RenewalAmount != nil {
				c, e := parseAdminMoney(*r.RenewalAmount)
				if e == nil {
					pageCents += c
				}
			}
		}
		result.PageRenewalSum = fmt.Sprintf("%d.%02d", pageCents/100, pageCents%100)
		return nil
	})
	return result, e
}

func decorateAdminRow(r *AdminInstanceRow, contact bool) {
	r.RenewalStale = r.RenewalAmount != nil && (r.RenewalPriceState != "verified" || time.Now().Unix()-r.RenewalSyncedAt > 4*3600)
	r.IPs = []string{}
	var ips []struct {
		Address string `json:"address"`
	}
	_ = json.Unmarshal([]byte(r.IPJSON), &ips)
	seen := map[string]bool{}
	for _, ip := range ips {
		if net.ParseIP(ip.Address) != nil && !seen[ip.Address] {
			r.IPs = append(r.IPs, ip.Address)
			seen[ip.Address] = true
		}
	}
	r.IPJSON = ""
	r.Config = []AdminConfigValue{}
	var values []AdminConfigValue
	_ = json.Unmarshal([]byte(r.ConfigJSON), &values)
	for _, v := range values {
		if adminConfigAllowed[v.Field] && utf8.ValidString(v.Value) && len(v.Value) <= 1024 {
			r.Config = append(r.Config, v)
		}
	}
	r.ConfigJSON = ""
	if !contact {
		if utf8.RuneCountInString(r.Phone) > 4 {
			chars := []rune(r.Phone)
			r.Phone = string(chars[:2]) + "****" + string(chars[len(chars)-2:])
		} else if r.Phone != "" {
			r.Phone = "****"
		}
		if at := strings.LastIndex(r.Email, "@"); at > 0 {
			r.Email = r.Email[:1] + "***" + r.Email[at:]
		} else if r.Email != "" {
			r.Email = "***"
		}
	}
}

type AdminFilterOption struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func (s *Service) AdminFilterOptions(ctx context.Context, a AdminAccess) (map[string][]AdminFilterOption, error) {
	out := map[string][]AdminFilterOption{}
	in := AdminListInput{}
	for _, kind := range []string{"customers", "products", "suppliers", "cycles"} {
		q := adminBase(s.db.WithContext(ctx), a, in)
		var fields string
		switch kind {
		case "customers":
			fields = "CAST(i.customer_id AS CHAR) id," + customerNameSQL + " name"
		case "products":
			fields = "CAST(i.product_id AS CHAR) id,i.product_name name"
		case "suppliers":
			q = q.Where("i.upstream_provider_id IS NOT NULL")
			fields = "CAST(i.upstream_provider_id AS CHAR) id,COALESCE(s.name,'通道未配置') name"
		case "cycles":
			fields = cycleSQL + " id," + cycleSQL + " name"
		}
		rows := []AdminFilterOption{}
		if e := q.Select("DISTINCT " + fields).Order("name ASC,id ASC").Limit(1000).Scan(&rows).Error; e != nil {
			return nil, e
		}
		out[kind] = rows
	}
	return out, nil
}
