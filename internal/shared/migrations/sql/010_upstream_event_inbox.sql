CREATE TABLE IF NOT EXISTS system.upstream_event_inbox (
 id BIGINT NOT NULL PRIMARY KEY,
 supplier_id BIGINT NOT NULL,
 instance_id BIGINT NOT NULL,
 body_hash CHAR(64) NOT NULL,
 deliveries BIGINT NOT NULL DEFAULT 1,
 first_seen DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
 last_seen DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
 UNIQUE KEY uk_supplier_body(supplier_id,body_hash),
 KEY idx_supplier_seen(supplier_id,last_seen)
) ENGINE=InnoDB;
-- Preserve ordering between concurrent authoritative reads within one second.
ALTER TABLE instance.instances MODIFY last_synced_at DATETIME(6) NULL;
