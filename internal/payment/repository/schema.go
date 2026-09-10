package payment

import (
	"context"
	"fmt"
	"strings"

	"ddyidc/internal/shared/snowflake"
	"gorm.io/gorm"
)

var paymentDefaults = []struct{ key, value, description string }{
	{"payment.wechat.enabled", "0", "微信支付开关"},
	{"payment.wechat.appid", "", "微信支付 AppID"},
	{"payment.wechat.mchid", "", "微信支付商户号"},
	{"payment.wechat.api_v3_key", "", "微信支付 API v3 密钥（凭据占位）"},
	{"payment.wechat.mch_private_key", "", "微信支付商户私钥（凭据占位）"},
	{"payment.wechat.cert_serial", "", "微信支付商户证书序列号"},
	{"payment.wechat.base_url", "http://127.0.0.1:8080", "微信支付回调公开基地址；生产必须 HTTPS"},
	{"payment.alipay.enabled", "0", "支付宝支付开关"},
	{"payment.alipay.app_id", "", "支付宝应用 ID（沙箱/正式凭据占位）"},
	{"payment.alipay.app_private_key", "", "支付宝应用私钥（凭据占位）"},
	{"payment.alipay.alipay_public_key", "", "支付宝公钥（凭据占位）"},
	{"payment.alipay.gateway", "https://openapi.alipaydev.com/gateway.do", "支付宝官方网关（当前为沙箱）"},
	{"payment.alipay.notify_url", "http://127.0.0.1:8080/api/v1/payment/notify/alipay", "支付宝异步通知地址；生产必须 HTTPS"},
	{"payment.alipay.return_url", "http://localhost:3100/console/orders", "支付宝同步返回地址（不作为支付结果依据）"},
	{"payment.expire_minutes", "15", "支付单过期分钟数"},
}

func EnsureSchemaAndDefaults(ctx context.Context, database *gorm.DB) error {
	for _, item := range paymentDefaults {
		if err := database.WithContext(ctx).Exec(
			"INSERT IGNORE INTO system.system_configs (id, `key`, value, description) VALUES (?, ?, ?, ?)",
			snowflake.GenID(), item.key, item.value, item.description,
		).Error; err != nil {
			return fmt.Errorf("seed %s: %w", item.key, err)
		}
	}
	columns := []struct{ name, ddl string }{
		{"biz_type", "ALTER TABLE payment.payments ADD COLUMN biz_type VARCHAR(16) NULL AFTER payment_no"},
		{"biz_id", "ALTER TABLE payment.payments ADD COLUMN biz_id BIGINT UNSIGNED NULL AFTER biz_type"},
		{"out_trade_no", "ALTER TABLE payment.payments ADD COLUMN out_trade_no VARCHAR(64) NULL AFTER channel"},
		{"transaction_id", "ALTER TABLE payment.payments ADD COLUMN transaction_id VARCHAR(128) NULL AFTER out_trade_no"},
		{"amount_fen", "ALTER TABLE payment.payments ADD COLUMN amount_fen BIGINT NULL AFTER amount"},
		{"currency", "ALTER TABLE payment.payments ADD COLUMN currency CHAR(3) NULL AFTER amount_fen"},
		{"notify_raw", "ALTER TABLE payment.payments ADD COLUMN notify_raw TEXT NULL AFTER status"},
		{"closed_at", "ALTER TABLE payment.payments ADD COLUMN closed_at DATETIME NULL AFTER paid_at"},
	}
	for _, column := range columns {
		exists, err := columnExists(ctx, database, "payment", "payments", column.name)
		if err != nil {
			return err
		}
		if !exists {
			if err := database.WithContext(ctx).Exec(column.ddl).Error; err != nil {
				return fmt.Errorf("add payment column %s: %w", column.name, err)
			}
		}
	}
	var amountType string
	if err := database.WithContext(ctx).Raw("SELECT DATA_TYPE FROM information_schema.columns WHERE table_schema='payment' AND table_name='payments' AND column_name='amount'").Scan(&amountType).Error; err != nil {
		return err
	}
	if !strings.EqualFold(amountType, "decimal") {
		if err := database.WithContext(ctx).Exec("UPDATE payment.payments SET amount_fen = amount WHERE amount_fen IS NULL OR amount_fen = 0").Error; err != nil {
			return err
		}
		if err := database.WithContext(ctx).Exec("ALTER TABLE payment.payments MODIFY COLUMN amount DECIMAL(10,2) NOT NULL COMMENT '支付金额（元）'").Error; err != nil {
			return err
		}
		if err := database.WithContext(ctx).Exec("UPDATE payment.payments SET amount = amount_fen / 100").Error; err != nil {
			return err
		}
	}
	var statusType string
	if err := database.WithContext(ctx).Raw("SELECT DATA_TYPE FROM information_schema.columns WHERE table_schema='payment' AND table_name='payments' AND column_name='status'").Scan(&statusType).Error; err != nil {
		return err
	}
	if !strings.EqualFold(statusType, "varchar") {
		if err := database.WithContext(ctx).Exec("ALTER TABLE payment.payments MODIFY COLUMN status VARCHAR(16) NOT NULL DEFAULT 'pending'").Error; err != nil {
			return err
		}
		if err := database.WithContext(ctx).Exec("UPDATE payment.payments SET status = CASE status WHEN '1' THEN 'pending' WHEN '2' THEN 'pending' WHEN '3' THEN 'paid' WHEN '4' THEN 'failed' WHEN '5' THEN 'refunded' WHEN '6' THEN 'closed' ELSE 'failed' END").Error; err != nil {
			return err
		}
	}
	backfill := "UPDATE payment.payments SET biz_type=COALESCE(NULLIF(biz_type,''),'order'), biz_id=COALESCE(biz_id,order_id), out_trade_no=COALESCE(NULLIF(out_trade_no,''),payment_no), amount_fen=COALESCE(amount_fen,ROUND(amount*100)), currency=COALESCE(NULLIF(currency,''),'CNY')"
	if err := database.WithContext(ctx).Exec(backfill).Error; err != nil {
		return err
	}
	for _, ddl := range []string{
		"ALTER TABLE payment.payments MODIFY COLUMN order_id BIGINT UNSIGNED NULL",
		"ALTER TABLE payment.payments MODIFY COLUMN biz_type VARCHAR(16) NOT NULL",
		"ALTER TABLE payment.payments MODIFY COLUMN biz_id BIGINT UNSIGNED NOT NULL",
		"ALTER TABLE payment.payments MODIFY COLUMN out_trade_no VARCHAR(64) NOT NULL",
		"ALTER TABLE payment.payments MODIFY COLUMN amount_fen BIGINT NOT NULL",
		"ALTER TABLE payment.payments MODIFY COLUMN currency CHAR(3) NOT NULL DEFAULT 'CNY'",
	} {
		if err := database.WithContext(ctx).Exec(ddl).Error; err != nil {
			return err
		}
	}
	if err := ensureIndex(ctx, database, "uk_out_trade_no", "ALTER TABLE payment.payments ADD UNIQUE KEY uk_out_trade_no (out_trade_no)"); err != nil {
		return err
	}
	return ensureIndex(ctx, database, "uk_biz", "ALTER TABLE payment.payments ADD UNIQUE KEY uk_biz (biz_type,biz_id)")
}

func columnExists(ctx context.Context, database *gorm.DB, schema, table, column string) (bool, error) {
	var count int64
	err := database.WithContext(ctx).Raw("SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=? AND table_name=? AND column_name=?", schema, table, column).Scan(&count).Error
	return count > 0, err
}

func ensureIndex(ctx context.Context, database *gorm.DB, name, ddl string) error {
	var count int64
	if err := database.WithContext(ctx).Raw("SELECT COUNT(*) FROM information_schema.statistics WHERE table_schema='payment' AND table_name='payments' AND index_name=?", name).Scan(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		return database.WithContext(ctx).Exec(ddl).Error
	}
	return nil
}
