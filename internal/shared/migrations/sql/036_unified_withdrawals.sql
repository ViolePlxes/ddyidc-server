CREATE TABLE IF NOT EXISTS payment.withdrawal_snapshots (
 withdrawal_id BIGINT NOT NULL PRIMARY KEY,
 customer_id BIGINT NOT NULL,
 source_module VARCHAR(32) NOT NULL DEFAULT 'finance',
 source_business_id VARCHAR(64) NOT NULL DEFAULT '',
 source_business_key VARCHAR(64) GENERATED ALWAYS AS (NULLIF(source_business_id, '')) STORED,
 UNIQUE KEY uk_withdrawal_source(source_module,source_business_key),
 fee DECIMAL(14,2) NOT NULL DEFAULT 0.00,
 net_amount DECIMAL(14,2) NOT NULL,
 policy_json JSON NOT NULL,
 created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
 KEY idx_withdrawal_snapshot_customer(customer_id,created_at)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS payment.withdrawal_notifications (
 withdrawal_id BIGINT NOT NULL PRIMARY KEY,
 customer_id BIGINT NOT NULL,
 amount DECIMAL(14,2) NOT NULL,
 result VARCHAR(16) NOT NULL,
 status VARCHAR(16) NOT NULL DEFAULT 'pending',
 lease_until DATETIME(3) NULL,
 updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
 KEY idx_withdrawal_notice(status,updated_at)
) ENGINE=InnoDB;

INSERT INTO system.permissions(id,code,name,module) VALUES
 (800000000003601,'withdrawal:view','查看提现账单','withdrawal'),
 (800000000003602,'withdrawal:review','审核提现申请','withdrawal'),
 (800000000003603,'withdrawal:account','查看提现收款信息','withdrawal'),
 (800000000003604,'withdrawal:remit','确认人工提现汇款','withdrawal')
 ON DUPLICATE KEY UPDATE id=id;
