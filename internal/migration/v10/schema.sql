-- Offline rehearsal only. No runtime callbacks or scheduled jobs.
CREATE TABLE IF NOT EXISTS system.v10_import_batches (
 source_sha256 CHAR(64) PRIMARY KEY, state VARCHAR(20) NOT NULL,
 created_at DATETIME NOT NULL, completed_at DATETIME NULL
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS system.v10_id_map (
 source_sha256 CHAR(64) NOT NULL, entity VARCHAR(32) NOT NULL,
 source_id BIGINT NOT NULL, target_id BIGINT NOT NULL,
 PRIMARY KEY(source_sha256,entity,source_id), UNIQUE(entity,target_id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS system.v10_customer_source (
 customer_id BIGINT PRIMARY KEY, source_id BIGINT NOT NULL,
 source_phone VARCHAR(100) NOT NULL, source_phone_code INT NOT NULL,
 source_name VARCHAR(100) NOT NULL, source_status INT NOT NULL,
 available_yuan DECIMAL(14,2) NOT NULL, frozen_yuan DECIMAL(14,2) NOT NULL,
 duplicate_phone BOOLEAN NOT NULL, source_sha256 CHAR(64) NOT NULL
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS system.v10_historical_orders (
 id BIGINT PRIMARY KEY, source_id BIGINT NOT NULL, customer_id BIGINT NOT NULL,
 order_no VARCHAR(64) NOT NULL UNIQUE, type VARCHAR(100) NOT NULL, status VARCHAR(25) NOT NULL,
 amount DECIMAL(14,2) NOT NULL, balance_amount DECIMAL(14,2) NOT NULL,
 unpaid_amount DECIMAL(14,2) NOT NULL, refund_amount DECIMAL(14,2) NOT NULL,
 gateway VARCHAR(100) NOT NULL, paid_at DATETIME NULL, created_at DATETIME NOT NULL,
 source_sha256 CHAR(64) NOT NULL, read_only BOOLEAN NOT NULL DEFAULT 1,
 INDEX(customer_id,created_at)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS system.v10_historical_order_items (
 id BIGINT PRIMARY KEY, source_id BIGINT NOT NULL, order_id BIGINT NOT NULL,
 source_host_id BIGINT NOT NULL, source_product_id BIGINT NOT NULL,
 type VARCHAR(100) NOT NULL, description TEXT NOT NULL,
 amount DECIMAL(14,2) NOT NULL, source_sha256 CHAR(64) NOT NULL,
 INDEX(order_id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS system.v10_instance_source (
 instance_id BIGINT PRIMARY KEY, source_id BIGINT NOT NULL,
 source_order_id BIGINT NOT NULL, source_product_id BIGINT NOT NULL,
 source_supplier_id BIGINT NOT NULL, source_remote_id VARCHAR(128) NOT NULL,
 first_payment_yuan DECIMAL(14,2) NOT NULL, renewal_yuan DECIMAL(14,2) NOT NULL,
 original_renewal DECIMAL(12,4) NOT NULL, cycle VARCHAR(100) NOT NULL,
 cycle_seconds BIGINT NOT NULL, source_sha256 CHAR(64) NOT NULL,
 read_only BOOLEAN NOT NULL DEFAULT 1
) ENGINE=InnoDB;
