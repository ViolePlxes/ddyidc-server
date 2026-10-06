CREATE TABLE IF NOT EXISTS instance.instance_upgrades (
 id BIGINT NOT NULL PRIMARY KEY, order_id BIGINT NOT NULL, instance_id BIGINT NOT NULL, customer_id BIGINT NOT NULL,
 ownership_version BIGINT NOT NULL, supplier_id BIGINT NOT NULL, host_id VARCHAR(64) NOT NULL,
 source_product_id BIGINT NOT NULL, target_product_id BIGINT NOT NULL, remote_product_id VARCHAR(64) NOT NULL,
 target_name VARCHAR(255) NOT NULL, cycle VARCHAR(32) NOT NULL, amount DECIMAL(14,2) NOT NULL, cost DECIMAL(14,2) NOT NULL,
 request_id VARCHAR(64) NOT NULL, invoice_id VARCHAR(64) NOT NULL DEFAULT '', state VARCHAR(32) NOT NULL,
 expires_at DATETIME(3) NOT NULL, created_at DATETIME(3) NOT NULL, updated_at DATETIME(3) NOT NULL,
 UNIQUE KEY uk_upgrade_order(order_id), UNIQUE KEY uk_upgrade_request(request_id), KEY idx_upgrade_host(instance_id,state)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
