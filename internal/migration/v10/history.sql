CREATE TABLE IF NOT EXISTS system.v10_history_records (
 id BIGINT PRIMARY KEY, source_id BIGINT NOT NULL, kind VARCHAR(20) NOT NULL,
 customer_id BIGINT NOT NULL, source_order_id BIGINT NOT NULL DEFAULT 0,
 source_ticket_id BIGINT NOT NULL DEFAULT 0,
 amount DECIMAL(14,2) NOT NULL DEFAULT 0, balance_after DECIMAL(14,2) NOT NULL DEFAULT 0,
 created_at DATETIME NOT NULL, payload JSON NOT NULL, payload_sha256 CHAR(64) NOT NULL,
 source_sha256 CHAR(64) NOT NULL, read_only BOOLEAN NOT NULL DEFAULT 1,
 UNIQUE(source_sha256,kind,source_id), INDEX(customer_id,kind,created_at),
 INDEX(customer_id,kind,source_ticket_id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS system.v10_history_batches (
 source_sha256 CHAR(64) PRIMARY KEY, completed_at DATETIME NOT NULL
) ENGINE=InnoDB;
