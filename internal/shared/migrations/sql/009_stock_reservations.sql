CREATE TABLE IF NOT EXISTS `order`.stock_reservations (
 order_id BIGINT NOT NULL PRIMARY KEY,
 product_id BIGINT NOT NULL,
 source_kind VARCHAR(16) NOT NULL,
 source_id BIGINT NOT NULL,
 quantity INT NOT NULL,
 status VARCHAR(16) NOT NULL,
 expires_at DATETIME(3) NOT NULL,
 created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
 updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
 KEY idx_expiry(status,expires_at)
) ENGINE=InnoDB;
