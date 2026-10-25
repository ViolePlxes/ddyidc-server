package domains

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"ddyidc/internal/shared/snowflake"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type ActionRequest struct {
	Action    string            `json:"action"`
	Params    map[string]string `json:"params"`
	RequestID string            `json:"requestId"`
	Confirmed bool              `json:"confirmed"`
}
type ActionResult struct {
	OperationID string `json:"operationId,omitempty"`
	Status      string `json:"status"`
	Data        any    `json:"data,omitempty"`
}

func (s *Service) authorize(ctx context.Context, db *gorm.DB, user int64, op OperationSpec, input map[string]string) error {
	if user <= 0 || op.Scope == "admin" {
		return ErrOwnership
	}
	if op.Scope == "owned" {
		var n int64
		query := db.WithContext(ctx).Model(&Asset{}).Where("customer_id=? AND domain=?", user, input["domain"])
		if op.Key != "5.3" && op.Key != "5.4" && op.Key != "5.5" {
			query = query.Where("status='active'")
		}
		if e := query.Count(&n).Error; e != nil {
			return e
		}
		if n != 1 {
			return ErrOwnership
		}
	}
	if op.Scope == "template" || input["c_sysid"] != "" || input["templateid"] != "" || input["templetid"] != "" {
		key := "c_sysid"
		for _, k := range []string{"c_sysid", "templateid", "templetid"} {
			if input[k] != "" {
				key = k
				break
			}
		}
		local, e := strconv.ParseInt(input[key], 10, 64)
		if e != nil || local <= 0 {
			return ErrOwnership
		}
		var row Template
		if e := db.WithContext(ctx).Take(&row, "id=? AND customer_id=? AND status<>'deleted'", local, user).Error; e != nil {
			return ErrOwnership
		}
		input[key] = row.ProviderID
	}
	return nil
}
func decodeData(raw json.RawMessage) any {
	var value any
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	_ = decoder.Decode(&value)
	return value
}
func stringValue(v any) string {
	switch v := v.(type) {
	case string:
		return v
	case json.Number:
		return v.String()
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	}
	return ""
}
func scrub(value any, secret bool) any {
	switch v := value.(type) {
	case map[string]any:
		result := map[string]any{}
		for key, item := range v {
			k := strings.ToLower(key)
			if strings.Contains(k, "token") || strings.Contains(k, "password") || strings.Contains(k, "api_") || k == "username" || k == "c_sysid" || k == "templateid" || k == "clientid" || k == "orgfile" || k == "f_code" || k == "f_sysid" {
				continue
			}
			if k == "domainpwd" && !secret {
				continue
			}
			if k == "cost" || k == "buyprice" || k == "renewprice" || k == "needprice" || k == "client_price" || k == "price" {
				continue
			}
			result[key] = scrub(item, secret)
		}
		return result
	case []any:
		result := make([]any, 0, len(v))
		for _, item := range v {
			result = append(result, scrub(item, secret))
		}
		return result
	default:
		return v
	}
}
func inputHash(action string, input map[string]string) string {
	raw, _ := json.Marshal(struct {
		Action string
		Input  map[string]string
	}{action, input})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func (s *Service) verifyRecord(ctx context.Context, cfg Settings, op OperationSpec, input map[string]string) error {
	if op.Key != "3.8" && op.Key != "3.9" {
		return nil
	}
	read, _ := operation("3.10")
	for page := 1; page <= 20; page++ {
		result, e := s.call(ctx, cfg, read, map[string]string{"domain": input["domain"], "limit": "100", "pageno": strconv.Itoa(page)})
		if e != nil {
			return e
		}
		value := decodeData(result.Data)
		var list []any
		switch v := value.(type) {
		case []any:
			list = v
		case map[string]any:
			list, _ = v["items"].([]any)
		}
		for _, item := range list {
			if row, ok := item.(map[string]any); ok && stringValue(row["id"]) == input["id"] {
				return nil
			}
		}
		if len(list) < 100 {
			break
		}
	}
	return ErrOwnership
}
func (s *Service) Execute(ctx context.Context, user int64, req ActionRequest) (*ActionResult, error) {
	op, e := operation(req.Action)
	if e != nil {
		return nil, e
	}
	if op.Paid {
		return nil, errors.New("收费操作请先取得报价并创建平台订单")
	}
	if op.Key == "4.9" {
		return nil, errors.New("实名上传凭证仅供服务端使用，请提交实名材料")
	}
	if op.Key == "3.3" {
		rows, e := s.Assets(ctx, user)
		return &ActionResult{Status: "succeeded", Data: rows}, e
	}
	if op.Key == "4.4" {
		rows, e := s.Templates(ctx, user)
		return &ActionResult{Status: "succeeded", Data: rows}, e
	}
	cfg, e := s.ready(ctx)
	if e != nil {
		return nil, e
	}
	input, e := validateInput(op, req.Params)
	if e != nil {
		return nil, e
	}
	if op.Key == "4.10" {
		if e := checkImage(input["file_org"]); e != nil {
			return nil, e
		}
	}
	if op.Write && !req.Confirmed {
		return nil, errors.New("请先确认本次操作")
	}
	// Ownership is checked before any request reaches the registrar.
	if e = s.authorize(ctx, s.db, user, op, input); e != nil {
		return nil, e
	}
	if e = s.verifyRecord(ctx, cfg, op, input); e != nil {
		return nil, e
	}
	if !op.Write {
		providerInput := input
		if op.Key == "1.1" {
			providerInput = map[string]string{"type": "domain", "value": input["value"], "year": "1"}
		}
		result, e := s.call(ctx, cfg, op, providerInput)
		if e != nil {
			return nil, e
		}
		value := decodeData(result.Data)
		if op.Key == "3.36" {
			owned, e := s.Assets(ctx, user)
			if e != nil {
				return nil, e
			}
			allowed := map[string]bool{}
			for _, asset := range owned {
				allowed[asset.Domain] = true
			}
			var list []any
			switch v := value.(type) {
			case []any:
				list = v
			case map[string]any:
				list, _ = v["items"].([]any)
			}
			if list == nil {
				return nil, ErrConnection
			}
			safe := []any{}
			for _, v := range list {
				row, ok := v.(map[string]any)
				if !ok {
					return nil, ErrConnection
				}
				domain, e := normalizeDomain(stringValue(row["strdomain"]))
				if e != nil {
					return nil, ErrConnection
				}
				if allowed[domain] {
					safe = append(safe, scrub(row, false))
				}
			}
			return &ActionResult{Status: "succeeded", Data: safe}, nil
		}
		if op.Key == "1.1" || op.Key == "3.14" || op.Key == "3.17" || op.Key == "5.2" || op.Key == "market.detail" {
			return s.retailResult(op, input, value, cfg)
		}
		if op.Key == "3.1" {
			value = retailAvailability(value, cfg)
		}
		return &ActionResult{Status: "succeeded", Data: scrub(value, op.Key == "3.4")}, nil
	}
	if !regexp.MustCompile(`^[A-Za-z0-9_-]{16,80}$`).MatchString(req.RequestID) {
		return nil, errors.New("请求标识无效，请刷新重试")
	}
	digest := inputHash(req.Action, req.Params)
	var saved Operation
	if e := s.db.WithContext(ctx).Take(&saved, "customer_id=? AND request_id=?", user, req.RequestID).Error; e == nil {
		if saved.InputHash != digest || saved.Action != req.Action {
			return nil, errors.New("请求标识与操作内容不一致")
		}
		return &ActionResult{OperationID: fmt.Sprint(saved.ID), Status: saved.Status, Data: decodeData(saved.Result)}, nil
	} else if !errors.Is(e, gorm.ErrRecordNotFound) {
		return nil, e
	}
	replay := false
	row := Operation{ID: snowflake.GenID(), CustomerID: user, Action: req.Action, Domain: input["domain"], RequestID: req.RequestID, InputHash: digest, Status: "executing", CreatedAt: s.now(), UpdatedAt: s.now()}
	e = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Account and resource rows serialize writes even before a template exists.
		var owner struct{ ID int64 }
		if e := tx.Table("customer.customers").Select("id").Clauses(clause.Locking{Strength: "UPDATE"}).Take(&owner, "id=?", user).Error; e != nil {
			return ErrOwnership
		}
		var old Operation
		if e := tx.Take(&old, "customer_id=? AND request_id=?", user, req.RequestID).Error; e == nil {
			if old.InputHash != digest || old.Action != req.Action {
				return errors.New("请求标识与操作内容不一致")
			}
			row = old
			replay = true
			return nil
		} else if !errors.Is(e, gorm.ErrRecordNotFound) {
			return e
		}
		if row.Domain != "" {
			var locked Asset
			if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Take(&locked, "customer_id=? AND domain=?", user, row.Domain).Error; e != nil {
				return ErrOwnership
			}
		}
		var pending int64
		if e := tx.Model(&Operation{}).Where("customer_id=? AND domain=? AND action=? AND status IN ('executing','unknown')", user, row.Domain, row.Action).Count(&pending).Error; e != nil {
			return e
		}
		if pending > 0 {
			return ErrPending
		}
		return tx.Create(&row).Error
	})
	if e != nil {
		return nil, e
	}
	if replay {
		return &ActionResult{OperationID: fmt.Sprint(row.ID), Status: row.Status, Data: decodeData(row.Result)}, nil
	}
	var result *ProviderResult
	if op.Key == "4.10" {
		result, e = s.upload(ctx, cfg, input)
	} else {
		result, e = s.call(ctx, cfg, op, input)
	}
	state := "submitted"
	errorCode := ""
	payload := json.RawMessage(`{}`)
	if e != nil {
		var failure *ProviderFailure
		if errors.As(e, &failure) {
			state = "failed"
			errorCode = failure.Code
		} else if errors.Is(e, ErrRate) {
			state = "failed"
			errorCode = "rate_limited"
		} else {
			state = "unknown"
			errorCode = "provider_unavailable"
		}
	} else {
		payload, _ = json.Marshal(scrub(decodeData(result.Data), false))
	}
	finish := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e == nil && op.Key == "4.1" {
			data, _ := decodeData(result.Data).(map[string]any)
			id := stringValue(data["c_sysid"])
			if !regexp.MustCompile(`^[0-9]{1,20}$`).MatchString(id) {
				state = "unknown"
				errorCode = "template_identity_missing"
			} else {
				t := Template{ID: snowflake.GenID(), CustomerID: user, ProviderID: id, Label: "注册信息模板", Status: "submitted", CreatedAt: s.now(), UpdatedAt: s.now()}
				if err := tx.Create(&t).Error; err != nil {
					return err
				}
				payload, _ = json.Marshal(t)
			}
		}
		if e == nil && op.Key == "4.3" {
			if err := tx.Model(&Template{}).Where("customer_id=? AND provider_id=?", user, input["c_sysid"]).Updates(map[string]any{"status": "deleted", "updated_at": s.now()}).Error; err != nil {
				return err
			}
		}
		return tx.Model(&Operation{}).Where("id=? AND status='executing'", row.ID).Updates(map[string]any{"status": state, "error_code": errorCode, "result": string(payload), "updated_at": s.now()}).Error
	})
	if finish != nil {
		return nil, ErrPending
	}
	if state == "unknown" {
		return nil, ErrPending
	}
	if e != nil {
		return nil, e
	}
	return &ActionResult{OperationID: fmt.Sprint(row.ID), Status: state, Data: decodeData(payload)}, nil
}
func checkImage(image string) error {
	raw, e := base64.StdEncoding.DecodeString(image)
	if e != nil || len(raw) == 0 || len(raw) > 4<<20 {
		return errors.New("请上传4MB以内的证件图片")
	}
	mime := http.DetectContentType(raw)
	if mime != "image/jpeg" && mime != "image/png" {
		return errors.New("仅支持JPEG或PNG图片")
	}
	return nil
}
func (s *Service) upload(ctx context.Context, cfg Settings, input map[string]string) (*ProviderResult, error) {
	tokenOp, _ := operation("4.9")
	result, e := s.call(ctx, cfg, tokenOp, map[string]string{"c_sysid": input["c_sysid"], "f_type_org": input["f_type_org"], "f_code_org": input["f_code_org"]})
	if e != nil {
		return nil, e
	}
	var token string
	if json.Unmarshal(result.Data, &token) != nil || token == "" {
		return nil, ErrConnection
	}
	return s.provider.Upload(ctx, token, input["file_org"])
}
