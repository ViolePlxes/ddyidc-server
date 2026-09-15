package certification

import (
	"context"
	orderdom "ddyidc/internal/order/domain"
	"ddyidc/internal/shared/snowflake"
	crypt "ddyidc/pkg/crypto"
	"encoding/json"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"strings"
	"time"
)

var errAlipayRate = errors.New("认证发起过于频繁：每分钟1次、每天最多3次，请稍后重试")

type AlipayResult struct {
	CertifyID       string `json:"certifyId,omitempty"`
	URL             string `json:"url,omitempty"`
	Status          int    `json:"status"`
	PaymentRequired bool   `json:"paymentRequired"`
	OrderID         string `json:"orderId,omitempty"`
	AmountCents     int64  `json:"amountCents,omitempty"`
}

func validateAlipayIdentity(in *Input) error {
	if err := Validate(in, 0); err != nil {
		return err
	}
	if in.DocumentType != 1 || !mainlandID.MatchString(in.DocumentNumber) {
		return errors.New("支付宝认证仅支持18位居民身份证")
	}
	weights := []int{7, 9, 10, 5, 8, 4, 2, 1, 6, 3, 7, 9, 10, 5, 8, 4, 2}
	sum := 0
	for i, w := range weights {
		sum += int(in.DocumentNumber[i]-'0') * w
	}
	if in.DocumentNumber[17] != "10X98765432"[sum%11] {
		return errors.New("身份证校验码无效")
	}
	birthday, err := time.Parse("20060102", in.DocumentNumber[6:14])
	if err != nil || birthday.After(time.Now()) || birthday.Year() < 1900 {
		return errors.New("身份证出生日期无效")
	}
	return nil
}

func (s *Service) attemptLimit(ctx context.Context, customer int64, operatorIP string) error {
	if s.redis == nil {
		return errors.New("安全服务暂不可用")
	}
	// Check all counters before incrementing any: failed minute checks don't consume daily quota.
	script := `for i,key in ipairs(KEYS) do if tonumber(redis.call('GET',key) or '0')>=tonumber(ARGV[i*2-1]) then return 0 end end; for i,key in ipairs(KEYS) do local n=redis.call('INCR',key);if n==1 then redis.call('EXPIRE',key,ARGV[i*2]) end end;return 1`
	prefix := fmt.Sprintf("certification:alipay:attempt:%d", customer)
	n, err := s.redis.Eval(ctx, script, []string{prefix + ":minute", prefix + ":daily", "certification:alipay:ip:" + s.hash(operatorIP)}, 1, 60, 3, 86400, 30, 86400).Int()
	if err != nil {
		return errors.New("安全服务暂不可用")
	}
	if n != 1 {
		return errAlipayRate
	}
	return nil
}

func (s *Service) initializeAlipay(ctx context.Context, customer int64, in Input, operatorIP, returnURL string) (AlipayResult, error) {
	var result AlipayResult
	if err := validateAlipayIdentity(&in); err != nil {
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
		if !fresh && (row.Status == 2 || row.CertifyChannel != "alipay") {
			return ErrUsed
		}
		cfg, err := LoadSettings(tx)
		if err != nil {
			return err
		}
		if !cfg.Enabled || cfg.Channel != "alipay" {
			return errors.New("支付宝认证通道暂未开放")
		}
		credentials, err := s.credentials(tx)
		if err != nil {
			return err
		}
		credentials.BizCode = cfg.BizCode
		client, err := s.alipayClient(credentials)
		if err != nil {
			return err
		}
		if fresh {
			row = Record{ID: snowflake.GenID(), CustomerID: customer, Type: 1, Status: 1, CertifyChannel: "alipay", CreatedAt: time.Now()}
		}
		// Paid attempts stay bound to the submitted identity until consumed.
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
				result = AlipayResult{Status: 1, PaymentRequired: true, OrderID: fmt.Sprint(order.ID), AmountCents: order.PayAmount}
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
			item := orderdom.OrderItem{OrderID: order.ID, ModuleType: "certification", ProductName: "支付宝实名认证手续费", ConfigSnapshot: `{"channel":"alipay"}`, Qty: 1, UnitPrice: fee, Subtotal: fee, CreatedAt: now, UpdatedAt: now}
			if err := tx.Create(&item).Error; err != nil {
				return err
			}
			row.CertifyFeeOrder = order.ID
			if err := tx.Save(&row).Error; err != nil {
				return err
			}
			result = AlipayResult{Status: 1, PaymentRequired: true, OrderID: fmt.Sprint(order.ID), AmountCents: fee}
			return audit(tx, customer, false, "fee_order", customer, map[string]any{"orderId": order.ID, "amountCents": fee}, operatorIP)
		}
		if err := s.attemptLimit(ctx, customer, operatorIP); err != nil {
			return err
		}
		certifyID, err := client.initialize(ctx, fmt.Sprintf("CERT%d", snowflake.GenID()), in.Name, in.DocumentNumber, returnURL)
		if err != nil {
			return err
		}
		encoded, _ := json.Marshal(credentials)
		snapshot, err := crypt.Encrypt(string(encoded), s.key)
		if err != nil {
			return err
		}
		address, err := client.certify(certifyID)
		if err != nil {
			return err
		}
		row.CertifyID = certifyID
		row.CertifyConfig = snapshot
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
		result = AlipayResult{Status: 1, CertifyID: certifyID, URL: address}
		return audit(tx, customer, false, "alipay_initialize", customer, map[string]any{"recordId": row.ID, "attempt": row.CertifyAttempts, "feeOrderId": consumed}, operatorIP)
	})
	return result, err
}

func (s *Service) alipayStatus(ctx context.Context, customer int64, operatorIP string, query bool) (AlipayResult, error) {
	var result AlipayResult
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row Record
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("customer_id=? AND certify_channel='alipay'", customer).Take(&row).Error; err != nil {
			return errors.New("尚未发起支付宝认证")
		}
		result.Status = row.Status
		if row.Status == 2 {
			return nil
		}
		if row.CertifyID == "" {
			return errors.New("请提交资料以发起支付宝认证")
		}
		plain, err := crypt.Decrypt(row.CertifyConfig, s.key)
		if err != nil {
			return errors.New("认证配置读取失败")
		}
		var cfg alipayCredentials
		if json.Unmarshal([]byte(plain), &cfg) != nil {
			return errors.New("认证配置读取失败")
		}
		client, err := s.alipayClient(cfg)
		if err != nil {
			return err
		}
		if !query {
			result.CertifyID = row.CertifyID
			result.URL, err = client.certify(row.CertifyID)
			return err
		}
		passed, err := client.query(ctx, row.CertifyID)
		if err != nil {
			return err
		}
		if passed {
			now := time.Now()
			if err := tx.Model(&row).Updates(map[string]any{"status": 2, "audited_at": now, "updated_at": now, "reject_reason": "", "certify_config": ""}).Error; err != nil {
				return err
			}
			result.Status = 2
			if err := audit(tx, customer, false, "alipay_auto_pass", customer, map[string]any{"recordId": row.ID, "status": 2}, operatorIP); err != nil {
				return err
			}
		}
		return audit(tx, customer, false, "alipay_query", customer, map[string]any{"recordId": row.ID, "passed": passed}, operatorIP)
	})
	return result, err
}

// Called only by the platform's verified payment dispatcher, never by HTTP input.
func (s *Service) ProvisionPlatformOrder(ctx context.Context, customer, orderID int64, method string) (bool, error) {
	var count int64
	if err := s.db.Table("order.order_items").Where("order_id=? AND module_type='certification'", orderID).Count(&count).Error; err != nil {
		return false, err
	}
	if count == 0 {
		return false, nil
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var order orderdom.Order
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND customer_id=?", orderID, customer).Take(&order).Error; err != nil {
			return err
		}
		if order.Status == orderdom.OrderStatusCompleted {
			return nil
		}
		if order.Status != orderdom.OrderStatusPaid {
			return errors.New("认证订单尚未支付")
		}
		if err := tx.Model(&order).Updates(map[string]any{"status": orderdom.OrderStatusCompleted, "updated_at": time.Now()}).Error; err != nil {
			return err
		}
		return audit(tx, customer, false, "fee_paid", customer, map[string]any{"orderId": orderID, "method": strings.TrimSpace(method)}, "")
	})
	return true, err
}

func (s *Service) alipayClient(credentials alipayCredentials) (*alipayClient, error) {
	client, err := newAlipayClient(credentials)
	if err == nil && s.alipayTransport != nil {
		client.http.Transport = s.alipayTransport
	}
	return client, err
}
