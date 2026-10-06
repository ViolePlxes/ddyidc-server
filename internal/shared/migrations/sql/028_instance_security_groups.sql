CREATE TABLE IF NOT EXISTS instance.instance_security_groups (
 id BIGINT NOT NULL PRIMARY KEY, instance_id BIGINT NOT NULL, customer_id BIGINT NOT NULL,
 ownership_version BIGINT NOT NULL, supplier_id BIGINT NOT NULL, host_id VARCHAR(64) NOT NULL,
 remote_id VARCHAR(64) NOT NULL DEFAULT '', name VARCHAR(80) NOT NULL, description VARCHAR(255) NOT NULL,
 request_id VARCHAR(64) NOT NULL, state VARCHAR(32) NOT NULL,
 created_at DATETIME(3) NOT NULL, updated_at DATETIME(3) NOT NULL,
 UNIQUE KEY uk_security_request(request_id), KEY idx_security_owner(customer_id,instance_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE TABLE IF NOT EXISTS instance.instance_security_commands (
 request_id VARCHAR(64) NOT NULL PRIMARY KEY, instance_id BIGINT NOT NULL,
 customer_id BIGINT NOT NULL, action VARCHAR(32) NOT NULL, payload JSON NOT NULL,
 state VARCHAR(32) NOT NULL, created_at DATETIME(3) NOT NULL,
 KEY idx_security_command_owner(instance_id,customer_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
