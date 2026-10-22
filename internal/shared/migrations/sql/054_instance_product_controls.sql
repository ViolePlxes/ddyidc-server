CREATE TABLE IF NOT EXISTS instance.instance_production_locks (
 instance_id BIGINT NOT NULL PRIMARY KEY,
 enabled BOOLEAN NOT NULL DEFAULT FALSE,
 updated_at DATETIME(3) NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE TABLE IF NOT EXISTS instance.instance_product_lifecycle (
 instance_id BIGINT NOT NULL, ownership_version BIGINT NOT NULL, base_expiry DATETIME(3) NOT NULL,
 kind VARCHAR(32) NOT NULL, state VARCHAR(32) NOT NULL, attempts INT NOT NULL DEFAULT 0,
 next_attempt_at DATETIME(3) NOT NULL, last_error VARCHAR(255) NOT NULL DEFAULT '', updated_at DATETIME(3) NOT NULL,
 PRIMARY KEY(instance_id,ownership_version,base_expiry,kind), KEY idx_product_lifecycle_retry(state,next_attempt_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
