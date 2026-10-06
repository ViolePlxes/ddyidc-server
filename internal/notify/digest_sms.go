package notify

import (
	"context"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/pkg/auth"
	"ddyidc/pkg/clientip"
	"encoding/json"
	dysms "github.com/alibabacloud-go/dysmsapi-20170525/v5/client"
	"github.com/alibabacloud-go/tea/dara"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"net/http"
	"strings"
	"time"
)

const digestSMSContent = "您有${server_count}台服务器即将到期，最早到期时间为${earliest_expiry}，请登录用户中心查看并及时续费。"

type smsApplication struct {
	Status    string    `json:"status"`
	Code      string    `json:"code"`
	RequestID string    `json:"requestId"`
	OrderID   string    `json:"orderId"`
	Error     string    `json:"error"`
	Content   string    `json:"content"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func (m *Manager) digestSMSApproved(ctx context.Context, code string) bool {
	values, e := m.repo.ConfigValues(ctx)
	if e != nil || code == "" {
		return false
	}
	var app smsApplication
	return json.Unmarshal([]byte(values["notify.digest.sms.application"]), &app) == nil && app.Status == "approved" && app.Code == code
}

func (h *adminHandler) applyDigestSMSTemplate(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r) {
		return
	}
	config, e := h.manager.loadConfig(r.Context())
	if e != nil || config.SMS.AccessKey == "" || config.SMS.AccessSecret == "" || config.SMS.SignName == "" {
		adminError(w, 400, 400, "请先配置短信账户和已审核签名")
		return
	}
	var in struct {
		Reason string `json:"reason"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in) != nil || in.Reason == "" {
		adminError(w, 400, 400, "请填写提交原因")
		return
	}
	cookie, _ := r.Cookie(h.auth.CookieName(auth.SessionAdmin))
	claims, e := h.auth.ParseAccessTokenFor(r.Context(), auth.SessionAdmin, cookie.Value)
	if e != nil {
		adminError(w, 401, 401, "登录已失效")
		return
	}
	app := smsApplication{Status: "applying", Content: digestSMSContent, UpdatedAt: time.Now().UTC()}
	payload, _ := json.Marshal(app)
	// This reservation is committed before the external API call. Unknown applications must be checked manually.
	e = h.manager.digest.db.WithContext(r.Context()).Transaction(func(tx *gorm.DB) error {
		if e := tx.Exec("INSERT IGNORE INTO system.system_configs (id,`key`,value,description) VALUES (?,'notify.digest.sms.application','','到期汇总短信审核')", snowflake.GenID()).Error; e != nil {
			return e
		}
		var row struct{ Value string }
		if e := tx.Table("system.system_configs").Clauses(clause.Locking{Strength: "UPDATE"}).Where("`key`='notify.digest.sms.application'").Take(&row).Error; e != nil {
			return e
		}
		if row.Value != "" {
			return gorm.ErrInvalidData
		}
		if e := NewRepository(tx).SaveConfig(r.Context(), "notify.digest.sms.application", string(payload)); e != nil {
			return e
		}
		return tx.Create(&DigestAttempt{ID: snowflake.GenID(), Status: "sms_template_apply", AdminID: claims.UserID, OperatorIP: clientip.FromRequest(r), Notes: truncate(in.Reason, 1024), CreatedAt: time.Now().UTC()}).Error
	})
	if e != nil {
		adminError(w, 409, 409, "已有模板申请，请查询审核结果，不要重复提交")
		return
	}
	client, e := (&aliyunSMSChannel{}).clientFor(config.SMS)
	if e == nil {
		request := (&dysms.CreateSmsTemplateRequest{}).SetTemplateName("服务器到期汇总提醒").SetTemplateContent(digestSMSContent).SetTemplateType(1).SetRelatedSignName(config.SMS.SignName).SetTemplateRule(`{"server_count":"other_number2","earliest_expiry":"time"}`).SetApplySceneContent("https://console.ddoidc.com").SetRemark("用于已购买服务器的客户到期服务通知，按用户合并，每日每用户最多一条，不含营销。示例：您有2台服务器即将到期，最早到期时间为2026-10-09 09:00，请登录用户中心查看并及时续费。")
		response, err := client.CreateSmsTemplateWithContext(r.Context(), request, &dara.RuntimeOptions{Autoretry: dara.Bool(false)})
		if err != nil || response == nil || response.Body == nil {
			app.Status = "unknown"
			app.Error = "申请结果待核对，请在阿里云控制台核对模板，禁止重复申请"
		} else {
			app.RequestID = stringValue(response.Body.RequestId)
			app.OrderID = stringValue(response.Body.OrderId)
			app.Code = stringValue(response.Body.TemplateCode)
			if stringValue(response.Body.Code) == "OK" && app.Code != "" {
				app.Status = "pending"
			} else {
				app.Status = "rejected"
				app.Error = "阿里云拒绝申请：" + stringValue(response.Body.Code)
			}
		}
	} else {
		app.Status = "rejected"
		app.Error = "短信客户端初始化失败"
	}
	app.UpdatedAt = time.Now().UTC()
	payload, _ = json.Marshal(app)
	saveContext, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
	defer cancel()
	if e = h.manager.repo.SaveConfig(saveContext, "notify.digest.sms.application", string(payload)); e != nil {
		adminError(w, 500, 500, "申请已执行，但保存结果失败，请核对阿里云记录")
		return
	}
	adminOK(w, app)
}

func (h *adminHandler) checkDigestSMSTemplate(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r) {
		return
	}
	values, e := h.manager.repo.ConfigValues(r.Context())
	var app smsApplication
	if e != nil {
		adminError(w, 500, 500, "读取配置失败")
		return
	}
	_ = json.Unmarshal([]byte(values["notify.digest.sms.application"]), &app)
	config, e := h.manager.loadConfig(r.Context())
	if e != nil {
		adminError(w, 500, 500, "读取配置失败")
		return
	}
	if app.Code == "" {
		app.Code = config.SMS.Templates["expiry_digest"]
	}
	if app.Code == "" {
		adminError(w, 400, 400, "没有可查询的模板编号，请先核对阿里云控制台")
		return
	}
	client, e := (&aliyunSMSChannel{}).clientFor(config.SMS)
	if e != nil {
		adminError(w, 500, 500, "短信客户端不可用")
		return
	}
	response, e := client.GetSmsTemplateWithContext(r.Context(), (&dysms.GetSmsTemplateRequest{}).SetTemplateCode(app.Code), &dara.RuntimeOptions{Autoretry: dara.Bool(false)})
	if e != nil || response == nil || response.Body == nil || stringValue(response.Body.Code) != "OK" {
		adminError(w, 502, 502, "读取阿里云审核结果失败，原配置已保留")
		return
	}
	status := stringValue(response.Body.TemplateStatus)
	app.Status = status
	app.UpdatedAt = time.Now().UTC()
	content := stringValue(response.Body.TemplateContent)
	if !strings.Contains(content, "${server_count}") || !strings.Contains(content, "${earliest_expiry}") || stringValue(response.Body.TemplateType) != "1" {
		adminError(w, 409, 409, "此模板不是匹配变量的到期汇总通知模板，未启用发送")
		return
	}
	app.Content = content
	// Aliyun reports 1 as approved, 0 as reviewing, and 2 as rejected.
	if status == "1" {
		if e = h.manager.repo.SaveConfig(r.Context(), "notify.sms.templates.expiry_digest", app.Code); e != nil {
			adminError(w, 500, 500, "保存已审核模板失败")
			return
		}
		app.Status = "approved"
	} else if status == "0" {
		app.Status = "pending"
	} else if status == "2" {
		app.Status = "rejected"
		app.Error = "审核未通过，请在阿里云控制台查看拒绝原因"
	}
	payload, _ := json.Marshal(app)
	if e = h.manager.repo.SaveConfig(r.Context(), "notify.digest.sms.application", string(payload)); e != nil {
		adminError(w, 500, 500, "保存审核状态失败")
		return
	}
	adminOK(w, app)
}
