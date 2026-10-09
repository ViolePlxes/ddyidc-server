package admintodo

import (
	billing "ddyidc/internal/billing/balance"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/pkg/auth"
	"ddyidc/pkg/clientip"
	"encoding/json"
	"github.com/zeromicro/go-zero/rest"
	"gorm.io/gorm"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type Settings struct {
	WithdrawalDailyCount  int      `json:"withdrawalDailyCount"`
	WithdrawalDailyAmount string   `json:"withdrawalDailyAmount"`
	WithdrawalSources     []string `json:"withdrawalSources"`
	WithdrawalMinimum     string   `json:"withdrawalMinimum"`
	WithdrawalMaximum     string   `json:"withdrawalMaximum"`
	OfflineEnabled        bool     `json:"offlineEnabled"`
	WithdrawalEnabled     bool     `json:"withdrawalEnabled"`
	WithdrawalFeePolicy   string   `json:"withdrawalFeePolicy"`
	OfflineInstructions   string   `json:"offlineInstructions"`
	ExpiringDays          int      `json:"expiringDays"`
}

func loadSettings(db *gorm.DB) (Settings, error) {
	out := Settings{ExpiringDays: 7, WithdrawalSources: []string{}, WithdrawalDailyCount: 3, WithdrawalDailyAmount: "50000.00", WithdrawalMinimum: "1.00", WithdrawalMaximum: "50000.00"}
	var rows []struct{ Key, Value string }
	if e := db.Table("system.system_configs").Where("`key` LIKE 'todos.%' OR `key`='notify.expiry.days_before'").Find(&rows).Error; e != nil {
		return out, e
	}
	for _, r := range rows {
		switch r.Key {
		case "todos.offline.enabled":
			out.OfflineEnabled = r.Value == "1" || strings.EqualFold(r.Value, "true")
		case "todos.withdrawals.daily_count":
			out.WithdrawalDailyCount, _ = strconv.Atoi(r.Value)
		case "todos.withdrawals.daily_amount":
			out.WithdrawalDailyAmount = r.Value
		case "todos.withdrawals.allowed_sources":
			if json.Unmarshal([]byte(r.Value), &out.WithdrawalSources) != nil {
				return out, ErrQuery
			}
		case "todos.withdrawals.minimum":
			out.WithdrawalMinimum = r.Value
		case "todos.withdrawals.maximum":
			out.WithdrawalMaximum = r.Value
		case "todos.withdrawals.fee_policy":
			out.WithdrawalFeePolicy = r.Value
		case "todos.withdrawals.enabled":
			out.WithdrawalEnabled = r.Value == "1" || strings.EqualFold(r.Value, "true")
		case "todos.offline.instructions":
			out.OfflineInstructions = r.Value
		case "notify.expiry.days_before":
			n, e := strconv.Atoi(r.Value)
			if e == nil && n >= 1 && n <= 90 {
				out.ExpiringDays = n
			}
		}
	}
	return out, nil
}
func registerSettings(server *rest.Server, db *gorm.DB, manager *auth.Manager) {
	server.AddRoute(rest.Route{Method: http.MethodGet, Path: "/api/v1/payment/offline-config", Handler: func(w http.ResponseWriter, r *http.Request) {
		out, e := loadSettings(db.WithContext(r.Context()))
		if e != nil {
			reply(w, 503, nil, "配置暂不可用")
			return
		}
		reply(w, 200, out, "")
	}})
	h := func(w http.ResponseWriter, r *http.Request) {
		c, e := manager.ParseAccessTokenFor(r.Context(), auth.SessionAdmin, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if e != nil || c == nil {
			reply(w, 401, nil, "请先登录")
			return
		}
		if !IsSuper(r.Context(), db, c.UserID) {
			reply(w, 403, nil, "仅超级管理员可配置")
			return
		}
		if r.Method == http.MethodGet {
			out, e := loadSettings(db.WithContext(r.Context()))
			if e != nil {
				reply(w, 503, nil, "配置暂不可用")
				return
			}
			reply(w, 200, out, "")
			return
		}
		var in Settings
		raw, readErr := io.ReadAll(http.MaxBytesReader(w, r.Body, 8192))
		if readErr != nil {
			reply(w, 400, nil, "配置请求无效")
			return
		}
		var provided map[string]json.RawMessage
		if json.Unmarshal(raw, &provided) != nil {
			reply(w, 400, nil, "配置请求无效")
			return
		}
		d := json.NewDecoder(strings.NewReader(string(raw)))
		d.DisallowUnknownFields()
		if d.Decode(&in) != nil || (in.WithdrawalFeePolicy != "" && in.WithdrawalFeePolicy != "free") || in.ExpiringDays < 1 || in.ExpiringDays > 90 || len([]rune(in.OfflineInstructions)) > 1000 {
			reply(w, 400, nil, "请填写有效的窗口天数与线下收款说明")
			return
		}
		existing, loadErr := loadSettings(db.WithContext(r.Context()))
		if loadErr != nil {
			reply(w, 503, nil, "现有配置暂不可用")
			return
		}
		if _, ok := provided["withdrawalSources"]; !ok {
			in.WithdrawalSources = existing.WithdrawalSources
		}
		if _, ok := provided["withdrawalMinimum"]; !ok {
			in.WithdrawalMinimum = existing.WithdrawalMinimum
		}
		if _, ok := provided["withdrawalMaximum"]; !ok {
			in.WithdrawalMaximum = existing.WithdrawalMaximum
		}
		if _, ok := provided["withdrawalDailyCount"]; !ok {
			in.WithdrawalDailyCount = existing.WithdrawalDailyCount
		}
		if _, ok := provided["withdrawalDailyAmount"]; !ok {
			in.WithdrawalDailyAmount = existing.WithdrawalDailyAmount
		}
		if in.WithdrawalMinimum == "" {
			in.WithdrawalMinimum = "1.00"
		}
		if in.WithdrawalMaximum == "" {
			in.WithdrawalMaximum = "50000.00"
		}
		if in.WithdrawalDailyCount == 0 {
			in.WithdrawalDailyCount = 3
		}
		if in.WithdrawalDailyAmount == "" {
			in.WithdrawalDailyAmount = "50000.00"
		}
		daily, de := billing.ParseMoney(in.WithdrawalDailyAmount)
		if de != nil || daily <= 0 || in.WithdrawalDailyCount < 1 || in.WithdrawalDailyCount > 100 {
			reply(w, 400, nil, "每日提现限制无效")
			return
		}
		min, me := billing.ParseMoney(in.WithdrawalMinimum)
		max, xe := billing.ParseMoney(in.WithdrawalMaximum)
		allowed := map[string]bool{"recharge": true, "commission": true, "marketplace_income": true, "manual_credit": true}
		for _, source := range in.WithdrawalSources {
			if !allowed[source] {
				reply(w, 400, nil, "提现来源无效")
				return
			}
		}
		if me != nil || xe != nil || min <= 0 || max < min {
			reply(w, 400, nil, "提现金额限制无效")
			return
		}
		sources, _ := json.Marshal(in.WithdrawalSources)
		boolText := func(b bool) string {
			if b {
				return "1"
			}
			return "0"
		}
		e = db.WithContext(r.Context()).Transaction(func(tx *gorm.DB) error {
			for k, v := range map[string]string{"todos.withdrawals.daily_count": strconv.Itoa(in.WithdrawalDailyCount), "todos.withdrawals.daily_amount": in.WithdrawalDailyAmount, "todos.withdrawals.allowed_sources": string(sources), "todos.withdrawals.minimum": in.WithdrawalMinimum, "todos.withdrawals.maximum": in.WithdrawalMaximum, "todos.offline.enabled": boolText(in.OfflineEnabled), "todos.withdrawals.enabled": boolText(in.WithdrawalEnabled), "todos.withdrawals.fee_policy": in.WithdrawalFeePolicy, "todos.offline.instructions": in.OfflineInstructions, "notify.expiry.days_before": strconv.Itoa(in.ExpiringDays)} {
				if err := tx.Exec("INSERT INTO system.system_configs(id,`key`,value) VALUES (?,?,?) ON DUPLICATE KEY UPDATE value=VALUES(value)", snowflake.GenID(), k, v).Error; err != nil {
					return err
				}
			}
			notes, _ := json.Marshal(map[string]any{"offlineEnabled": in.OfflineEnabled, "withdrawalEnabled": in.WithdrawalEnabled, "withdrawalDailyCount": in.WithdrawalDailyCount, "withdrawalDailyAmount": in.WithdrawalDailyAmount, "withdrawalSources": in.WithdrawalSources, "withdrawalMinimum": in.WithdrawalMinimum, "withdrawalMaximum": in.WithdrawalMaximum, "withdrawalFeePolicy": in.WithdrawalFeePolicy, "expiringDays": in.ExpiringDays, "instructionsConfigured": strings.TrimSpace(in.OfflineInstructions) != ""})
			return tx.Table("system.todo_audits").Create(map[string]any{"id": snowflake.GenID(), "request_id": strconv.FormatInt(snowflake.GenID(), 10), "kind": "settings", "source": "settings", "object_id": 0, "action": "configure", "admin_id": c.UserID, "operator_ip": clientip.FromRequest(r), "notes": string(notes), "created_at": time.Now().UTC()}).Error
		})
		if e != nil {
			reply(w, 503, nil, "配置保存失败")
			return
		}
		reply(w, 200, in, "")
	}
	server.AddRoute(rest.Route{Method: http.MethodGet, Path: "/admin/v1/todos/settings", Handler: h})
	server.AddRoute(rest.Route{Method: http.MethodPut, Path: "/admin/v1/todos/settings", Handler: h})
}
