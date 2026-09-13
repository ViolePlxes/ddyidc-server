package adminread

import (
	"bytes"
	balance "ddyidc/internal/billing/balance"
	"encoding/json"
	"gorm.io/gorm"
	"io"
	"net/http"
	"strings"
	"time"
)

func (h *handler) clientDetail(w http.ResponseWriter, r *http.Request) {
	id, ok := h.detailID(w, r)
	if !ok {
		return
	}
	var row customerRow
	if e := h.customerQuery(r).Select(customerProjection).Where("c.id=?", id).Take(&row).Error; e != nil {
		writeError(w, 500, "读取客户失败")
		return
	}
	account := balance.Account{}
	if e := h.db.Where("customer_id=?", id).Find(&account).Error; e != nil {
		writeError(w, 500, "读取余额失败")
		return
	}
	settings := customerSettings{ReceiveSMS: true, ReceiveEmail: true}
	if e := h.db.Where("customer_id=?", id).Find(&settings).Error; e != nil {
		writeError(w, 500, "读取偏好失败")
		return
	}
	logs := []struct {
		IP        string    `json:"ip"`
		CreatedAt time.Time `json:"create_time"`
	}{}
	if e := h.db.Table("customer.login_logs").Select("ip,created_at").Where("customer_id=? AND status=1", id).Order("created_at DESC,id DESC").Limit(5).Scan(&logs).Error; e != nil {
		writeError(w, 500, "读取登录记录失败")
		return
	}
	var login struct {
		LastLoginAt *time.Time
		LastLoginIP string
	}
	if e := h.db.Table("customer.customers").Select("last_login_at,COALESCE(last_login_ip,'') last_login_ip").Where("id=?", id).Take(&login).Error; e != nil {
		writeError(w, 500, "读取登录信息失败")
		return
	}
	oauth := []string{}
	if row.SocialLogin != "" {
		oauth = strings.Split(row.SocialLogin, ",")
	}
	phoneCode, phone := "", row.Phone
	if phone != "" {
		phoneCode = "+86"
		if strings.HasPrefix(phone, "+") {
			for _, code := range []string{"+297", "+389", "+420", "+421", "+423", "+500", "+501", "+502", "+503", "+504", "+505", "+506", "+507", "+508", "+509", "+590", "+591", "+592", "+593", "+594", "+595", "+596", "+387", "+386", "+385", "+383", "+350", "+351", "+352", "+353", "+354", "+355", "+356", "+357", "+358", "+359", "+597", "+370", "+372", "+373", "+374", "+375", "+376", "+377", "+378", "+380", "+381", "+382", "+371", "+598", "+599", "+670", "+886", "+960", "+961", "+962", "+963", "+964", "+965", "+966", "+967", "+968", "+880", "+970", "+972", "+973", "+974", "+975", "+976", "+977", "+992", "+993", "+994", "+995", "+971", "+299", "+856", "+853", "+672", "+673", "+674", "+675", "+676", "+677", "+678", "+679", "+680", "+681", "+855", "+682", "+685", "+686", "+687", "+688", "+689", "+690", "+691", "+692", "+850", "+852", "+683", "+298", "+998", "+291", "+239", "+238", "+237", "+236", "+235", "+234", "+233", "+232", "+231", "+230", "+229", "+228", "+227", "+226", "+225", "+224", "+223", "+222", "+221", "+220", "+996", "+216", "+213", "+212", "+211", "+240", "+241", "+218", "+243", "+242", "+290", "+269", "+267", "+266", "+265", "+264", "+263", "+262", "+261", "+260", "+258", "+257", "+268", "+255", "+256", "+245", "+247", "+248", "+249", "+246", "+251", "+252", "+253", "+254", "+250", "+244", "+51", "+41", "+49", "+48", "+47", "+46", "+45", "+44", "+43", "+32", "+39", "+36", "+34", "+33", "+31", "+30", "+27", "+20", "+52", "+40", "+53", "+82", "+55", "+98", "+95", "+94", "+93", "+92", "+91", "+90", "+86", "+84", "+54", "+81", "+65", "+64", "+63", "+62", "+61", "+60", "+58", "+57", "+56", "+66", "+7", "+1"} {
				if strings.HasPrefix(phone, code) {
					phoneCode = code
					phone = strings.TrimPrefix(phone, code)
					break
				}
			}
		}
	}
	writeJSON(w, map[string]any{"id": row.ID, "username": row.Name, "account": row.Username, "email": row.Email, "phone": phone, "phone_code": phoneCode, "company": row.Company, "country_id": row.Country, "address": row.Address, "language": row.Language, "notes": settings.Remark, "status": row.Status, "register_time": row.CreatedAt, "last_login_time": login.LastLoginAt, "last_login_ip": login.LastLoginIP, "balance": account.Balance, "frozen_balance": account.FrozenBalance, "consume": account.TotalConsume, "refund": account.TotalRefund, "withdraw": account.TotalWithdraw, "host_num": row.ProductCount, "host_active_num": row.ActiveProductCount, "login_logs": logs, "certification": row.RealnameStatus, "receive_sms": settings.ReceiveSMS, "receive_email": settings.ReceiveEmail, "oauth": oauth})
}
func (h *handler) clientUpdate(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Username        string `json:"username"`
		Email           string `json:"email"`
		PhoneCode       string `json:"phone_code"`
		Phone           string `json:"phone"`
		Company         string `json:"company"`
		Country         string `json:"country_id"`
		Address         string `json:"address"`
		Language        string `json:"language"`
		Notes           string `json:"notes"`
		Password        string `json:"password"`
		OperatePassword string `json:"operate_password"`
		ReceiveSMS      bool   `json:"receive_sms"`
		ReceiveEmail    bool   `json:"receive_email"`
	}
	if e := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384)).Decode(&input); e != nil {
		writeError(w, 400, "资料格式错误")
		return
	}
	if input.Phone != "" && input.PhoneCode == "" {
		writeError(w, 400, "手机号必须带国家区号")
		return
	}
	converted := updateCustomerInput{createCustomerInput: createCustomerInput{Name: input.Username, Email: input.Email, CountryCode: input.PhoneCode, Mobile: input.Phone, Password: input.Password}, Company: input.Company, Country: input.Country, Address: input.Address, Language: input.Language, Remark: input.Notes, OperationPassword: input.OperatePassword, ReceiveSMS: input.ReceiveSMS, ReceiveEmail: input.ReceiveEmail}
	raw, _ := json.Marshal(converted)
	r.Body = io.NopCloser(bytes.NewReader(raw))
	h.updateCustomer(w, r)
}

func profileChangedFields(tx *gorm.DB, id int64, in updateCustomerInput, n normalizedCustomerInput) (string, error) {
	var old struct {
		Name, Email, Phone, Company, Country, Address, Language, Remark string
		ReceiveSMS, ReceiveEmail                                        bool
	}
	err := tx.Table("customer.customers c").Select("COALESCE(c.real_name,c.nickname,'') name,COALESCE(c.email,'') email,COALESCE(c.phone,'') phone,COALESCE(p.company,'') company,COALESCE(p.country,'') country,COALESCE(p.address,'') address,c.language,COALESCE(s.remark,'') remark,COALESCE(s.receive_sms,1) receive_sms,COALESCE(s.receive_email,1) receive_email").Joins("LEFT JOIN customer.customer_profiles p ON p.customer_id=c.id").Joins("LEFT JOIN customer.admin_customer_settings s ON s.customer_id=c.id").Where("c.id=?", id).Take(&old).Error
	if err != nil {
		return "", err
	}
	fields := []string{}
	comparisons := []struct{ key, before, after string }{{"name", old.Name, n.Name}, {"email", old.Email, n.Email}, {"phone", old.Phone, n.Phone}, {"company", old.Company, in.Company}, {"country", old.Country, in.Country}, {"address", old.Address, in.Address}, {"language", old.Language, in.Language}, {"notes", old.Remark, in.Remark}}
	for _, field := range comparisons {
		if field.before != field.after {
			fields = append(fields, field.key)
		}
	}
	if old.ReceiveSMS != in.ReceiveSMS {
		fields = append(fields, "receive_sms")
	}
	if old.ReceiveEmail != in.ReceiveEmail {
		fields = append(fields, "receive_email")
	}
	if in.Password != "" {
		fields = append(fields, "password_changed")
	}
	if in.OperationPassword != "" {
		fields = append(fields, "operate_password_changed")
	}
	raw, _ := json.Marshal(fields)
	return string(raw), nil
}
