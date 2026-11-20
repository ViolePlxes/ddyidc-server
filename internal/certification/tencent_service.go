package certification

import (
	"context"
	"crypto/subtle"
	invoice "ddyidc/internal/billing/api"
	orderdom "ddyidc/internal/order/domain"
	"ddyidc/internal/shared/snowflake"
	crypt "ddyidc/pkg/crypto"
	"encoding/json"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"net/url"
	"strings"
	"time"
)

// errTencentRate shares the支付宝 attempt wording: the limits are identical.
var errTencentRate = errAlipayRate

const tencentMaxFailures = 3

type TencentResult struct {
	BizToken        string `json:"bizToken,omitempty"`
	URL             string `json:"url,omitempty"`
	Status          int    `json:"status"`
	PaymentRequired bool   `json:"paymentRequired"`
	OrderID         string `json:"orderId,omitempty"`
	AmountCents     int64  `json:"amountCents,omitempty"`
}

// tencentSnapshot is the encrypted per-record copy. It deliberately carries no
// SecretId/SecretKey: those are read live from the encrypted configuration.
type tencentSnapshot struct {
	Provider  string `json:"provider"`
	RuleID    string `json:"ruleId"`
	BizToken  string `json:"bizToken,omitempty"`
	URL       string `json:"url,omitempty"`
	RequestID string `json:"requestId,omitempty"`
	Failures  int    `json:"failures"`
}

// tencentStoredResult is what certify_result holds once decided: a verdict, no
// identity data and no faces.
type tencentStoredResult struct {
	Provider      string    `json:"provider"`
	RequestID     string    `json:"requestId"`
	ErrCode       int       `json:"errCode"`
	LiveStatus    int       `json:"liveStatus"`
	CompareStatus int       `json:"compareStatus"`
	Sim           float64   `json:"sim"`
	Threshold     float64   `json:"threshold"`
	DecidedAt     time.Time `json:"decidedAt"`
}

// tencentRedirect builds the callback URL from the already verified request
// origin. When redirectBase is configured the origin must sit under it.
func tencentRedirect(origin string, credentials tencentCredentials) (string, error) {
	origin = strings.TrimSuffix(strings.TrimSpace(origin), "/")
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Host == "" || parsed.Path != "" || parsed.RawQuery != "" || parsed.User != nil {
		return "", errors.New("请求来源不允许")
	}
	switch parsed.Scheme {
	case "https":
	case "http":
		host := strings.Split(parsed.Host, ":")[0]
		if host != "localhost" && host != "127.0.0.1" {
			return "", errors.New("请求来源不允许")
		}
	default:
		return "", errors.New("请求来源不允许")
	}
	if base := credentials.RedirectBase; base != "" && origin != base && !strings.HasPrefix(origin, base+"/") {
		return "", errors.New("回跳地址不在允许范围内")
	}
	target := origin + "/api/v1/certification/tencent/callback"
	if len(target) > 1024 {
		return "", errors.New("回跳地址过长")
	}
	return target, nil
}

func (s *Service) tencentClient(credentials tencentCredentials) (*tencentClient, error) {
	client, err := newTencentClient(credentials)
	if err == nil && s.tencentTransport != nil {
		client.http.Transport = s.tencentTransport
	}
	return client, err
}

func validateFaceIdentity(in *Input) error { return validateAlipayIdentity(in) }

func (s *Service) initializeTencent(ctx context.Context, customer int64, in Input, operatorIP, origin string) (TencentResult, error) {
	var result TencentResult
	if err := validateFaceIdentity(&in); err != nil {
		return result, err
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var owner struct{ ID, TenantID int64 }
		if err := tx.Table("customer.customers").Clauses(clause.Locking{Strength: "UPDATE"}).Select("id,tenant_id").Where("id=? AND status=1 AND deleted_at IS NULL", customer).Take(&owner).Error; err != nil {
			return errors.New("客户不存在或已停用")
		}
		var row Record
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("customer_id=?", customer).Take(&row).Error
		fresh := errors.Is(err, gorm.ErrRecordNotFound)
		if err != nil && !fresh {
			return err
		}
		if !fresh && row.Status == 3 {
			return errors.New("认证未通过，请联系客服重新申请")
		}
		if !fresh && (row.Status == 2 || row.CertifyChannel != "tencent") {
			return ErrUsed
		}
		cfg, err := LoadSettings(tx)
		if err != nil {
			return err
		}
		if !cfg.Enabled || cfg.Channel != "tencent" {
			return errors.New("腾讯慧眼认证通道暂未开放")
		}
		credentials, err := s.tencentCredentials(tx)
		if err != nil {
			return err
		}
		if !credentials.configured() {
			return errors.New("腾讯慧眼认证尚未配置，请联系客服")
		}
		client, err := s.tencentClient(credentials)
		if err != nil {
			return err
		}
		redirect, err := tencentRedirect(origin, credentials)
		if err != nil {
			return err
		}
		if fresh {
			row = Record{ID: snowflake.GenID(), CustomerID: customer, Type: 1, Status: 1, CertifyChannel: "tencent", CreatedAt: time.Now()}
		}
		// A paid attempt stays bound to the submitted identity until consumed.
		if !fresh && (row.DocumentHash != s.hash(in.DocumentNumber) || row.Name != in.Name) {
			return errors.New("重新认证须使用原姓名和证件号码，避免与已发起认证混淆")
		}
		encrypted, err := crypt.Encrypt(in.DocumentNumber, s.key)
		if err != nil {
			return err
		}
		row.Name = in.Name
		row.Phone = in.Phone
		row.DocumentType = 1
		row.IDCardNo = encrypted
		row.DocumentHash = s.hash(in.DocumentNumber)
		row.UpdatedAt = time.Now()
		fee, err := feeCents(cfg.Fee)
		if err != nil {
			return err
		}
		if row.CertifyFeeOrder != 0 {
			var order orderdom.Order
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND customer_id=?", row.CertifyFeeOrder, customer).Take(&order).Error; err != nil {
				return errors.New("认证订单读取失败")
			}
			switch order.Status {
			case orderdom.OrderStatusPending:
				result = TencentResult{Status: 1, PaymentRequired: true, OrderID: fmt.Sprint(order.ID), AmountCents: order.PayAmount}
				return nil
			case orderdom.OrderStatusCompleted, orderdom.OrderStatusPaid:
			default:
				row.CertifyFeeOrder = 0
			}
		}
		if row.CertifyAttempts >= cfg.FreeAttempts && fee > 0 && row.CertifyFeeOrder == 0 {
			now := time.Now()
			order := orderdom.Order{ID: snowflake.GenID(), TenantID: owner.TenantID, CustomerID: customer, TotalAmount: fee, PayAmount: fee, Status: orderdom.OrderStatusPending, CreatedAt: now, UpdatedAt: now}
			order.OrderNo = fmt.Sprintf("CERT%d", order.ID)
			if err := tx.Create(&order).Error; err != nil {
				return err
			}
			item := orderdom.OrderItem{OrderID: order.ID, ModuleType: "certification", ProductName: "腾讯慧眼实名认证手续费", ConfigSnapshot: `{"channel":"tencent"}`, Qty: 1, UnitPrice: fee, Subtotal: fee, CreatedAt: now, UpdatedAt: now}
			if err := tx.Create(&item).Error; err != nil {
				return err
			}
			if e := invoice.IssueOrderTx(tx, item.OrderID); e != nil {
				return e
			}
			row.CertifyFeeOrder = order.ID
			if err := tx.Save(&row).Error; err != nil {
				return err
			}
			result = TencentResult{Status: 1, PaymentRequired: true, OrderID: fmt.Sprint(order.ID), AmountCents: fee}
			return audit(tx, customer, false, "fee_order", customer, map[string]any{"orderId": order.ID, "amountCents": fee, "channel": "tencent"}, operatorIP)
		}
		if err := s.attemptLimitFor(ctx, "tencent", customer, operatorIP); err != nil {
			return err
		}
		auth, err := client.detectAuth(ctx, in.Name, in.DocumentNumber, redirect, fmt.Sprintf("certifyId=%d", row.ID))
		if err != nil {
			return errors.New(tencentFailureMessage(err))
		}
		// Consecutive failures survive a re-initialization, so a retry loop cannot
		// reset the rejection counter.
		failures := 0
		if !fresh && strings.TrimSpace(row.CertifyConfig) != "" {
			if plain, decryptErr := crypt.Decrypt(row.CertifyConfig, s.key); decryptErr == nil {
				var previous tencentSnapshot
				if json.Unmarshal([]byte(plain), &previous) == nil {
					failures = previous.Failures
				}
			}
		}
		snapshot := tencentSnapshot{Provider: "tencent", RuleID: credentials.RuleID, BizToken: auth.BizToken, URL: auth.URL, RequestID: auth.RequestID, Failures: failures}
		encoded, err := json.Marshal(snapshot)
		if err != nil {
			return errors.New("认证配置读取失败")
		}
		sealed, err := crypt.Encrypt(string(encoded), s.key)
		if err != nil {
			return err
		}
		expires := time.Now().Add(tencentTokenTTL)
		// A keyed digest binds the provider token to this record without storing
		// the token itself in a readable column.
		row.CertifyID = s.hash("tencent:" + auth.BizToken)
		row.CertifyConfig = sealed
		row.CertifyExpiresAt = &expires
		row.CertifyAttempts++
		row.Status = 1
		consumed := row.CertifyFeeOrder
		row.CertifyFeeOrder = 0
		if err := tx.Save(&row).Error; err != nil {
			return err
		}
		if consumed != 0 {
			if err := tx.Table("order.order_items").Where("order_id=? AND module_type='certification'", consumed).Updates(map[string]any{"delivery_status": 2, "updated_at": time.Now()}).Error; err != nil {
				return err
			}
		}
		result = TencentResult{Status: 1, BizToken: auth.BizToken, URL: auth.URL}
		return audit(tx, customer, false, "tencent_initialize", customer, map[string]any{"recordId": row.ID, "attempt": row.CertifyAttempts, "feeOrderId": consumed, "requestId": auth.RequestID}, operatorIP)
	})
	return result, err
}

func (s *Service) tencentStatus(ctx context.Context, customer int64, operatorIP string, query bool) (TencentResult, error) {
	var result TencentResult
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row Record
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("customer_id=? AND certify_channel='tencent'", customer).Take(&row).Error; err != nil {
			return errors.New("尚未发起腾讯慧眼认证")
		}
		result.Status = row.Status
		if row.Status == 2 || row.Status == 3 {
			return nil
		}
		if strings.TrimSpace(row.CertifyConfig) == "" {
			return errors.New("请提交资料以发起腾讯慧眼认证")
		}
		plain, err := crypt.Decrypt(row.CertifyConfig, s.key)
		if err != nil {
			return errors.New("认证配置读取失败")
		}
		var snapshot tencentSnapshot
		if json.Unmarshal([]byte(plain), &snapshot) != nil {
			return errors.New("认证配置读取失败")
		}
		// The snapshot must be the one bound to this record's token digest.
		if row.CertifyID == "" || s.hash("tencent:"+snapshot.BizToken) != row.CertifyID {
			return errors.New("认证配置读取失败")
		}
		if !query {
			if snapshot.URL == "" {
				return errors.New("请重新发起腾讯慧眼认证")
			}
			result.URL = snapshot.URL
			return nil
		}
		if row.CertifyExpiresAt != nil && time.Now().After(*row.CertifyExpiresAt) {
			return errors.New("核身已过期，请重新发起")
		}
		if snapshot.BizToken == "" {
			return errors.New("核身已过期，请重新发起")
		}
		credentials, err := s.tencentCredentials(tx)
		if err != nil {
			return err
		}
		if !credentials.configured() {
			return errors.New("腾讯慧眼认证尚未配置，请联系客服")
		}
		client, err := s.tencentClient(credentials)
		if err != nil {
			return err
		}
		text, requestID, err := client.detectInfo(ctx, snapshot.BizToken)
		if err != nil {
			return errors.New(tencentFailureMessage(err))
		}
		if snapshot.RequestID == "" {
			snapshot.RequestID = requestID
		}
		decision := decideTencent(text, credentials.SimThreshold)
		now := time.Now()
		stored := tencentStoredResult{Provider: "tencent", RequestID: requestID, ErrCode: decision.ErrCode, LiveStatus: decision.LiveStatus, CompareStatus: decision.CompareStatus, Sim: decision.Sim, Threshold: credentials.SimThreshold, DecidedAt: now}
		if decision.Passed {
			encoded, err := json.Marshal(stored)
			if err != nil {
				return errors.New("认证结论保存失败")
			}
			sealed, err := crypt.Encrypt(string(encoded), s.key)
			if err != nil {
				return err
			}
			if err := tx.Model(&row).Updates(map[string]any{"status": 2, "audited_at": now, "updated_at": now, "reject_reason": "", "certify_config": "", "certify_result": sealed}).Error; err != nil {
				return err
			}
			result.Status = 2
			if err := audit(tx, customer, false, "tencent_auto_pass", customer, map[string]any{"recordId": row.ID, "status": 2, "requestId": requestID, "sim": decision.Sim, "threshold": credentials.SimThreshold}, operatorIP); err != nil {
				return err
			}
			return audit(tx, customer, false, "tencent_query", customer, map[string]any{"recordId": row.ID, "passed": true, "requestId": requestID, "errCode": decision.ErrCode, "liveStatus": decision.LiveStatus, "compareStatus": decision.CompareStatus, "sim": decision.Sim}, operatorIP)
		}
		// A failed comparison keeps the record retryable until the threshold is
		// reached; it never passes automatically.
		snapshot.Failures++
		encoded, err := json.Marshal(snapshot)
		if err != nil {
			return errors.New("认证配置读取失败")
		}
		sealed, err := crypt.Encrypt(string(encoded), s.key)
		if err != nil {
			return err
		}
		updates := map[string]any{"updated_at": now, "certify_config": sealed}
		if snapshot.Failures >= tencentMaxFailures {
			encodedResult, _ := json.Marshal(stored)
			resultSealed, err := crypt.Encrypt(string(encodedResult), s.key)
			if err != nil {
				return err
			}
			updates["status"] = 3
			updates["audited_at"] = now
			updates["reject_reason"] = "人脸核身未通过，请联系客服"
			updates["certify_result"] = resultSealed
			result.Status = 3
		}
		if err := tx.Model(&row).Updates(updates).Error; err != nil {
			return err
		}
		return audit(tx, customer, false, "tencent_query", customer, map[string]any{"recordId": row.ID, "passed": false, "reason": decision.Reason, "requestId": requestID, "errCode": decision.ErrCode, "liveStatus": decision.LiveStatus, "compareStatus": decision.CompareStatus, "sim": decision.Sim, "failures": snapshot.Failures}, operatorIP)
	})
	return result, err
}

// tencentVerifyToken proves the redirect token belongs to this customer's
// pending record. It never changes state: only tencentStatus decides.
func (s *Service) tencentVerifyToken(ctx context.Context, customer int64, token string) error {
	var row Record
	if err := s.db.WithContext(ctx).Where("customer_id=? AND certify_channel='tencent'", customer).Take(&row).Error; err != nil {
		return errors.New("核身记录不存在")
	}
	if row.Status != 1 || row.CertifyID == "" {
		return errors.New("核身记录不存在")
	}
	if subtle.ConstantTimeCompare([]byte(s.hash("tencent:"+token)), []byte(row.CertifyID)) != 1 {
		return errors.New("核身记录不存在")
	}
	return nil
}
