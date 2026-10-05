CREATE TABLE IF NOT EXISTS payment.offline_transfers (
 id BIGINT NOT NULL PRIMARY KEY,
 request_id VARCHAR(64) NOT NULL UNIQUE,
 customer_id BIGINT NOT NULL,
 order_id BIGINT NOT NULL,
 payment_id BIGINT NOT NULL,
 amount DECIMAL(14,2) NOT NULL,
 payer VARCHAR(128) NOT NULL,
 transaction_no VARCHAR(128) NOT NULL,
 transferred_at DATETIME(3) NOT NULL,
 evidence VARCHAR(512) NOT NULL,
 evidence_id VARCHAR(64) NOT NULL DEFAULT '',
 status VARCHAR(16) NOT NULL DEFAULT 'pending',
 active_transaction VARCHAR(128) GENERATED ALWAYS AS (CASE WHEN status='rejected' THEN NULL ELSE transaction_no END) STORED,
 UNIQUE KEY uk_offline_active_transaction(active_transaction),
 revision BIGINT NOT NULL DEFAULT 1,
 reason VARCHAR(500) NOT NULL DEFAULT '',
 admin_id BIGINT NOT NULL DEFAULT 0,
 operator_ip VARCHAR(45) NOT NULL DEFAULT '',
 created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
 updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
 KEY idx_offline_todo(status,created_at,id),
 KEY idx_offline_customer(customer_id,order_id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS payment.todo_withdrawal_accounts (
 source VARCHAR(32) NOT NULL,
 withdrawal_id BIGINT NOT NULL,
 customer_id BIGINT NOT NULL,
 ciphertext VARCHAR(512) NOT NULL,
 request_id VARCHAR(64) NOT NULL UNIQUE,
 created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
 PRIMARY KEY(source,withdrawal_id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS system.todo_event_resolutions (
 event_id BIGINT NOT NULL PRIMARY KEY,
 instance_id BIGINT NOT NULL,
 request_id VARCHAR(64) NOT NULL UNIQUE,
 admin_id BIGINT NOT NULL,
 reason VARCHAR(256) NOT NULL,
 evidence VARCHAR(400) NOT NULL,
 facts JSON NOT NULL,
 created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
 KEY idx_todo_resolution_instance(instance_id,created_at)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS marketing.reward_batches (
 biz_type TINYINT NOT NULL,
 order_id BIGINT NOT NULL,
 customer_id BIGINT NOT NULL,
 base_amount BIGINT NOT NULL,
 settle_mode VARCHAR(16) NOT NULL,
 rule_snapshot JSON NOT NULL,
 created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
 PRIMARY KEY(biz_type,order_id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS payment.todo_evidence_files (
 id VARCHAR(64) NOT NULL PRIMARY KEY,
 customer_id BIGINT NOT NULL,
 mime VARCHAR(64) NOT NULL,
 size BIGINT NOT NULL,
 checksum CHAR(64) NOT NULL,
 ciphertext MEDIUMTEXT NOT NULL,
 created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
 KEY idx_todo_evidence_customer(customer_id,created_at)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS payment.manual_remittances (
 transaction_no VARCHAR(128) NOT NULL PRIMARY KEY,
 source VARCHAR(32) NOT NULL,
 object_id BIGINT NOT NULL,
 request_id VARCHAR(64) NOT NULL UNIQUE,
 created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
 UNIQUE KEY uk_remittance_object(source,object_id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS system.todo_audits (
 id BIGINT NOT NULL PRIMARY KEY,
 request_id VARCHAR(64) NOT NULL UNIQUE,
 kind VARCHAR(32) NOT NULL,
 source VARCHAR(32) NOT NULL,
 object_id BIGINT NOT NULL,
 action VARCHAR(32) NOT NULL,
 admin_id BIGINT NOT NULL,
 operator_ip VARCHAR(45) NOT NULL,
 notes VARCHAR(500) NOT NULL,
 evidence VARCHAR(512) NOT NULL DEFAULT '',
 created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
 KEY idx_todo_object(kind,source,object_id,created_at)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS payment.balance_withdrawals (
 id BIGINT NOT NULL PRIMARY KEY,
 request_id VARCHAR(64) NOT NULL UNIQUE,
 customer_id BIGINT NOT NULL,
 amount DECIMAL(14,2) NOT NULL,
 account_note VARCHAR(512) NOT NULL,
 status VARCHAR(16) NOT NULL DEFAULT 'pending',
 revision BIGINT NOT NULL DEFAULT 1,
 reason VARCHAR(500) NOT NULL DEFAULT '',
 transaction_no VARCHAR(128) NULL UNIQUE,
 evidence VARCHAR(512) NOT NULL DEFAULT '',
 admin_id BIGINT NOT NULL DEFAULT 0,
 operator_ip VARCHAR(45) NOT NULL DEFAULT '',
 created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
 updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
 KEY idx_balance_withdraw_todo(status,created_at,id),
 KEY idx_balance_withdraw_customer(customer_id,created_at)
) ENGINE=InnoDB;
