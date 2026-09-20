package adminread

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"ddyidc/internal/shared/snowflake"
	"ddyidc/pkg/auth"
	"github.com/zeromicro/go-zero/rest/pathvar"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type customerSettings struct {
	OperationPasswordHash string `json:"-"`
	CustomerID            int64  `gorm:"primaryKey" json:"-"`
	Remark                string `json:"remark"`
	ReceiveSMS            bool   `json:"receiveSms"`
	ReceiveEmail          bool   `json:"receiveEmail"`
}

func (customerSettings) TableName() string { return "customer.admin_customer_settings" }

func (h *handler) detailID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	if !h.authorize(w, r) {
		return 0, false
	}
	id, e := strconv.ParseInt(pathvar.Vars(r)["id"], 10, 64)
	if e != nil || id <= 0 {
		writeError(w, 400, "客户 ID 无效")
		return 0, false
	}
	var count int64
	if e = h.db.WithContext(r.Context()).Table("customer.customers").Where("id=? AND deleted_at IS NULL", id).Count(&count).Error; e != nil {
		writeError(w, 500, "读取客户失败")
		return 0, false
	}
	if count == 0 {
		writeError(w, 404, "客户不存在")
		return 0, false
	}
	return id, true
}
func (h *handler) customerProfile(w http.ResponseWriter, r *http.Request) {
	id, ok := h.detailID(w, r)
	if !ok {
		return
	}
	var profile struct {
		Name    string `json:"name"`
		Company string `json:"company"`
		Address string `json:"address"`
	}
	if e := h.customerQuery(r).Select("COALESCE(c.real_name,c.nickname,'') name,COALESCE(cp.company,'') company,COALESCE(cp.address,'') address").Where("c.id=?", id).Take(&profile).Error; e != nil {
		writeError(w, 500, "读取资料失败")
		return
	}
	settings := customerSettings{CustomerID: id, ReceiveSMS: true, ReceiveEmail: true}
	if e := h.db.WithContext(r.Context()).Where("customer_id=?", id).Take(&settings).Error; e != nil && !errors.Is(e, gorm.ErrRecordNotFound) {
		writeError(w, 500, "读取客户偏好失败，请确认数据库迁移已执行")
		return
	}
	var realname []struct {
		Name         string    `json:"name"`
		CompanyName  string    `json:"companyName"`
		Type         int       `json:"type"`
		Status       int       `json:"status"`
		RejectReason string    `json:"rejectReason"`
		CreatedAt    time.Time `json:"createdAt"`
	}
	if e := h.db.WithContext(r.Context()).Table("customer.customer_realname").Select("name,COALESCE(company_name,'') company_name,type,status,COALESCE(reject_reason,'') reject_reason,created_at").Where("customer_id=?", id).Order("id DESC").Limit(1).Scan(&realname).Error; e != nil {
		writeError(w, 500, "读取实名信息失败")
		return
	}
	writeJSON(w, map[string]any{"name": profile.Name, "company": profile.Company, "address": profile.Address, "settings": settings, "operationPasswordSet": settings.OperationPasswordHash != "", "realname": realname})
}

type updateCustomerInput struct {
	OperationPassword string `json:"operationPassword"`
	createCustomerInput
	Company      string `json:"company"`
	Country      string `json:"country"`
	Address      string `json:"address"`
	Language     string `json:"language"`
	Remark       string `json:"remark"`
	ReceiveSMS   bool   `json:"receiveSms"`
	ReceiveEmail bool   `json:"receiveEmail"`
}

func validateProfile(in updateCustomerInput) (normalizedCustomerInput, string) {
	if in.OperationPassword != "" && (len(in.OperationPassword) < 6 || len(in.OperationPassword) > 20) {
		return normalizedCustomerInput{}, "操作密码须为 6-20 位"
	}
	if in.Mobile != "" && strings.TrimSpace(in.CountryCode) == "" {
		return normalizedCustomerInput{}, "手机号必须带国家区号"
	}
	base := in.createCustomerInput
	if base.Password == "" {
		base.Password = "unchanged"
	}
	normalized, message := normalizeCustomerInput(base)
	if message != "" {
		return normalized, message
	}
	if utf8.RuneCountInString(in.Address) > 255 || utf8.RuneCountInString(in.Remark) > 1000 || utf8.RuneCountInString(in.Company) > 128 || utf8.RuneCountInString(in.Country) > 64 {
		return normalized, "资料字段超出长度限制"
	}
	if in.Language != "zh-CN" && in.Language != "zh-TW" && in.Language != "en" {
		return normalized, "请选择支持的语言"
	}
	for _, c := range strings.TrimSpace(in.Mobile) {
		if c < '0' || c > '9' {
			return normalized, "手机号只能包含数字"
		}
	}
	return normalized, ""
}
func (h *handler) updateCustomer(w http.ResponseWriter, r *http.Request) {
	id, ok := h.detailID(w, r)
	if !ok {
		return
	}
	var in updateCustomerInput
	if e := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); e != nil {
		writeError(w, 400, "资料格式错误")
		return
	}
	n, message := validateProfile(in)
	if message != "" {
		writeError(w, 400, message)
		return
	}
	updates := map[string]any{"real_name": n.Name, "email": nil, "phone": nil, "language": in.Language, "updated_at": time.Now()}
	if n.Email != "" {
		updates["email"] = n.Email
	}
	if n.Phone != "" {
		updates["phone"] = n.Phone
	}
	if in.Password != "" {
		hash, e := auth.HashPassword(in.Password)
		if e != nil {
			writeError(w, 500, "密码加密失败")
			return
		}
		updates["password_hash"] = hash
		if e = h.auth.RevokeSession(r.Context(), auth.SessionUser, id); e != nil {
			writeError(w, 500, "撤销用户会话失败，密码未修改")
			return
		}
	}
	e := h.db.WithContext(r.Context()).Transaction(func(tx *gorm.DB) error {
		changedFields, err := profileChangedFields(tx, id, in, n)
		if err != nil {
			return err
		}
		if e := tx.Table("customer.customers").Where("id=? AND deleted_at IS NULL", id).Updates(updates).Error; e != nil {
			return e
		}
		profile := map[string]any{"id": snowflake.GenID(), "customer_id": id, "company": in.Company, "country": in.Country, "address": in.Address, "created_at": time.Now(), "updated_at": time.Now()}
		if e := tx.Table("customer.customer_profiles").Clauses(clause.OnConflict{DoUpdates: clause.AssignmentColumns([]string{"company", "country", "address", "updated_at"})}).Create(profile).Error; e != nil {
			return e
		}
		settings := customerSettings{CustomerID: id, Remark: in.Remark, ReceiveSMS: in.ReceiveSMS, ReceiveEmail: in.ReceiveEmail}
		columns := []string{"remark", "receive_sms", "receive_email"}
		if in.OperationPassword != "" {
			hash, err := auth.HashPassword(in.OperationPassword)
			if err != nil {
				return err
			}
			settings.OperationPasswordHash = hash
			columns = append(columns, "operation_password_hash")
		}
		if e := tx.Clauses(clause.OnConflict{DoUpdates: clause.AssignmentColumns(columns)}).Create(&settings).Error; e != nil {
			return e
		}
		actor, name, e := h.balanceActor(r)
		if e != nil {
			return e
		}
		return auditCustomer(tx, id, actor, name, "profile_update", changedFields, requestIP(r))
	})
	if e != nil {
		if strings.Contains(strings.ToLower(e.Error()), "duplicate") {
			writeError(w, 409, "手机号或邮箱已被其他用户使用")
		} else {
			writeError(w, 500, "保存资料失败")
		}
		return
	}
	writeJSON(w, map[string]bool{"saved": true})
}
func (h *handler) customerRecords(w http.ResponseWriter, r *http.Request) {
	id, ok := h.detailID(w, r)
	if !ok {
		return
	}
	page, size := pageParams(r)
	kind := pathvar.Vars(r)["kind"]
	var query *gorm.DB
	switch kind {
	case "products":
		query = h.db.WithContext(r.Context()).Table("instance.instances").Where("customer_id=? AND deleted_at IS NULL AND status<>6", id)
		if r.URL.Query().Get("active") == "true" {
			query = query.Where("status IN (1,2,3,4)")
		}
	case "orders":
		query = h.orderQuery(r, nil, "").Where("o.customer_id=?", id)
	case "transactions":
		query = h.db.WithContext(r.Context()).Table("billing.transactions").Where("customer_id=?", id)
	case "logins":
		query = h.db.WithContext(r.Context()).Table("customer.login_logs").Where("customer_id=? AND status=1", id)
	default:
		writeError(w, 404, "记录类型不存在")
		return
	}
	var total int64
	if e := query.Session(&gorm.Session{}).Count(&total).Error; e != nil {
		writeError(w, 500, "读取记录数量失败")
		return
	}
	var selection, order string
	switch kind {
	case "products":
		selection = "CAST(id AS CHAR) id,instance_no number,product_name name,status,COALESCE(primary_ip,'') ip,remark,created_at,activated_at,expires_at"
		order = "created_at DESC,id DESC"
	case "orders":
		selection = "CAST(o.id AS CHAR) id,o.order_no number,COALESCE(oi.product_name,'') name,o.status,o.pay_amount amount,o.created_at"
		order = "o.created_at DESC,o.id DESC"
	case "transactions":
		selection = "CAST(id AS CHAR) id,tx_no number,CASE type WHEN 'recharge' THEN 1 WHEN 'order_pay' THEN 2 WHEN 'refund' THEN 3 WHEN 'withdraw' THEN 4 WHEN 'commission' THEN 5 ELSE 6 END type,direction,CAST(ABS(amount)*100 AS SIGNED) amount,CAST(balance_before*100 AS SIGNED) balance_before,CAST(balance_after*100 AS SIGNED) balance_after,COALESCE(remark,'') remark,created_at"
		order = "created_at DESC,id DESC"
	case "logins":
		selection = "CAST(id AS CHAR) id,ip,created_at"
		order = "created_at DESC,id DESC"
	}
	rows := []customerRecord{}
	if e := query.Select(selection).Order(order).Offset((page - 1) * size).Limit(size).Find(&rows).Error; e != nil {
		writeError(w, 500, "读取记录失败")
		return
	}
	writeJSON(w, map[string]any{"items": rows, "total": total, "page": page, "pageSize": size})
}
func (h *handler) customerStatus(w http.ResponseWriter, r *http.Request) {
	id, ok := h.detailID(w, r)
	if !ok {
		return
	}
	var in struct {
		Status int `json:"status"`
	}
	if e := json.NewDecoder(r.Body).Decode(&in); e != nil || (in.Status != 1 && in.Status != 2) {
		writeError(w, 400, "状态无效")
		return
	}
	if e := h.auth.RevokeSession(r.Context(), auth.SessionUser, id); e != nil {
		writeError(w, 500, "撤销用户会话失败")
		return
	}
	if e := h.db.WithContext(r.Context()).Table("customer.customers").Where("id=? AND deleted_at IS NULL", id).Updates(map[string]any{"status": in.Status, "updated_at": time.Now()}).Error; e != nil {
		writeError(w, 500, "更新状态失败")
		return
	}
	writeJSON(w, map[string]bool{"saved": true})
}

type customerRecord struct {
	ID            string     `json:"id"`
	Number        string     `json:"number"`
	Name          string     `json:"name"`
	Status        int        `json:"status"`
	IP            string     `json:"ip"`
	Type          int        `json:"type"`
	Direction     int        `json:"direction"`
	Amount        int64      `json:"amount"`
	BalanceBefore int64      `json:"balance_before"`
	BalanceAfter  int64      `json:"balance_after"`
	Remark        string     `json:"remark"`
	CreatedAt     time.Time  `json:"created_at"`
	ActivatedAt   *time.Time `json:"activated_at"`
	ExpiresAt     *time.Time `json:"expires_at"`
}

func (h *handler) deleteCustomer(w http.ResponseWriter, r *http.Request) {
	id, ok := h.detailID(w, r)
	if !ok {
		return
	}
	protected := errors.New("客户有关联业务记录或资金账户，不能删除；可改为停用")
	e := h.db.WithContext(r.Context()).Transaction(func(tx *gorm.DB) error {

		var row struct{ ID int64 }
		if e := tx.Table("customer.customers").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND deleted_at IS NULL", id).Take(&row).Error; e != nil {
			return e
		}
		// Inspect customer references so extension-module records also block deletion.
		var references []struct {
			SchemaName string
			TableName  string
			ColumnName string
		}
		if e := tx.Table("information_schema.COLUMNS").Select("TABLE_SCHEMA schema_name,TABLE_NAME table_name,COLUMN_NAME column_name").Where("TABLE_SCHEMA NOT IN ('mysql','sys','information_schema','performance_schema') AND COLUMN_NAME IN ('customer_id','client_id','user_id','buyer_id','seller_id','inviter_id','invitee_id')").Scan(&references).Error; e != nil {
			return e
		}
		for _, ref := range references {
			if ref.SchemaName == "customer" && (ref.TableName == "customer_profiles" || ref.TableName == "admin_customer_settings" || ref.TableName == "login_logs") {
				continue
			}
			quote := func(v string) string { return "`" + strings.ReplaceAll(v, "`", "``") + "`" }
			var count int64
			if e := tx.Table(quote(ref.SchemaName)+"."+quote(ref.TableName)).Where(quote(ref.ColumnName)+"=?", id).Count(&count).Error; e != nil {
				return e
			}
			if count > 0 {
				return protected
			}
		}
		if e := h.auth.RevokeSession(r.Context(), auth.SessionUser, id); e != nil {
			return e
		}
		return tx.Table("customer.customers").Where("id=?", id).Updates(map[string]any{"status": 3, "deleted_at": time.Now(), "updated_at": time.Now()}).Error
	})
	if errors.Is(e, protected) {
		writeError(w, 409, e.Error())
		return
	}
	if e != nil {
		writeError(w, 500, "删除用户失败")
		return
	}
	writeJSON(w, map[string]bool{"deleted": true})
}
