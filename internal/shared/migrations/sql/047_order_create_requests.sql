-- Additive request identity; retained across upgrades and rollback. No financial data changes.
CREATE TABLE IF NOT EXISTS `order`.create_requests (
 customer_id BIGINT UNSIGNED NOT NULL,
 request_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 fingerprint CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 order_id BIGINT UNSIGNED NOT NULL DEFAULT 0,
 created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
 PRIMARY KEY(customer_id,request_id),
 KEY idx_order(order_id)
) ENGINE=InnoDB;
