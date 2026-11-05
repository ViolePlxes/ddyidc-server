-- Explicit renewal contract prices. One active override per instance/cycle at a
-- time; revoked rows are retained for audit. No order, payment or supplier
-- operation happens here.
CREATE TABLE IF NOT EXISTS billing.renewal_price_overrides (
 id BIGINT NOT NULL PRIMARY KEY,
 customer_id BIGINT UNSIGNED NOT NULL,
 instance_id BIGINT UNSIGNED NOT NULL,
 cycle VARCHAR(32) NOT NULL DEFAULT '',
 mode VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 value DECIMAL(14,2) NOT NULL,
 effective_from DATETIME(6) NOT NULL,
 effective_to DATETIME(6) NULL,
 reason VARCHAR(255) NOT NULL,
 status VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT 'active',
 created_by BIGINT UNSIGNED NOT NULL,
 created_at DATETIME(6) NOT NULL,
 updated_at DATETIME(6) NOT NULL,
 KEY idx_override_instance (instance_id, cycle, status),
 KEY idx_override_customer (customer_id),
 CHECK(value>0),
 CHECK(effective_to IS NULL OR effective_to>effective_from)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

INSERT IGNORE INTO system.permissions(id,code,name,module) VALUES
 (947430628107,'instance:edit','实例价格编辑','instance');
