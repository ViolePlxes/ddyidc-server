CREATE TABLE IF NOT EXISTS instance.instance_renewal_settings (
 instance_id BIGINT NOT NULL PRIMARY KEY, customer_id BIGINT NOT NULL,
 enabled BOOLEAN NOT NULL DEFAULT FALSE, updated_at DATETIME(3) NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE TABLE IF NOT EXISTS instance.instance_renewals (
 id BIGINT NOT NULL PRIMARY KEY, order_id BIGINT NOT NULL, instance_id BIGINT NOT NULL,
 customer_id BIGINT NOT NULL, ownership_version BIGINT NOT NULL DEFAULT 0,
 supplier_id BIGINT NOT NULL, host_id VARCHAR(64) NOT NULL,
 cycle VARCHAR(32) NOT NULL, duration BIGINT NOT NULL,
 amount DECIMAL(14,2) NOT NULL, cost DECIMAL(14,2) NOT NULL,
 base_expiry DATETIME(3) NOT NULL, invoice_id VARCHAR(64) NOT NULL DEFAULT '',
 state VARCHAR(32) NOT NULL, last_error VARCHAR(255) NOT NULL DEFAULT '',
 created_at DATETIME(3) NOT NULL, updated_at DATETIME(3) NOT NULL,
 UNIQUE KEY uk_renewal_order(order_id), KEY idx_renewal_instance(instance_id), KEY idx_renewal_state(state)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
