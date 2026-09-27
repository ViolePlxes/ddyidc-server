CREATE TABLE IF NOT EXISTS `order`.delivery_units (
 id BIGINT NOT NULL PRIMARY KEY,
 order_id BIGINT NOT NULL,
 order_item_id BIGINT NOT NULL,
 unit_no INT NOT NULL,
 customer_id BIGINT NOT NULL,
 instance_id BIGINT NOT NULL UNIQUE,
 status VARCHAR(16) NOT NULL DEFAULT 'pending',
 created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
 updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
 UNIQUE KEY uk_item_unit(order_item_id,unit_no),
 KEY idx_order_status(order_id,status)
) ENGINE=InnoDB;
-- Each unit owns its own remote invoice; legacy order-level records remain intact.
CREATE TABLE IF NOT EXISTS `order`.unit_procurements (
 id BIGINT UNSIGNED NOT NULL PRIMARY KEY,
 order_id BIGINT UNSIGNED NOT NULL,
 supplier_id BIGINT UNSIGNED NOT NULL,
 upstream_product_id INT NOT NULL,
 upstream_order_id VARCHAR(100) NOT NULL,
 upstream_host_id VARCHAR(100) NULL,
 status VARCHAR(32) NOT NULL,
 cost DECIMAL(14,2) NOT NULL DEFAULT 0,
 balance_before DECIMAL(14,2) NOT NULL DEFAULT 0,
 balance_after DECIMAL(14,2) NOT NULL DEFAULT 0,
 attempt_count TINYINT UNSIGNED NOT NULL DEFAULT 0,
 last_error VARCHAR(512) NULL,
 created_at BIGINT NOT NULL,
 updated_at BIGINT NOT NULL,
 KEY idx_order(order_id)
) ENGINE=InnoDB;
