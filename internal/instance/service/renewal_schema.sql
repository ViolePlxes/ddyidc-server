CREATE TABLE IF NOT EXISTS instance.instance_renewal_settings (
 instance_id BIGINT NOT NULL PRIMARY KEY, customer_id BIGINT NOT NULL,
 enabled BOOLEAN NOT NULL DEFAULT FALSE, updated_at DATETIME(3) NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE TABLE IF NOT EXISTS instance.instance_renewals (
 id BIGINT NOT NULL PRIMARY KEY, order_id BIGINT NOT NULL, instance_id BIGINT NOT NULL,
 customer_id BIGINT NOT NULL, ownership_version BIGINT NOT NULL DEFAULT 0,
 supplier_id BIGINT NOT NULL, host_id VARCHAR(64) NOT NULL,
 cycle VARCHAR(32) NOT NULL, duration BIGINT NOT NULL,
 amount DECIMAL(14,2) NOT NULL, cost DECIMAL(14,2) NOT NULL,
 base_expiry DATETIME(3) NOT NULL, invoice_id VARCHAR(64) NOT NULL DEFAULT '',
 state VARCHAR(32) NOT NULL, last_error VARCHAR(255) NOT NULL DEFAULT '',
 created_at DATETIME(3) NOT NULL, updated_at DATETIME(3) NOT NULL,
 UNIQUE KEY uk_renewal_order(order_id), KEY idx_renewal_instance(instance_id), KEY idx_renewal_state(state)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

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

CREATE TABLE IF NOT EXISTS payment.machine_refund_actions (
 application_id BIGINT NOT NULL PRIMARY KEY, customer_id BIGINT NOT NULL, order_id BIGINT NOT NULL,
 request_id VARCHAR(64) NOT NULL, action VARCHAR(32) NOT NULL, state VARCHAR(32) NOT NULL,
 amount DECIMAL(14,2) NOT NULL, admin_id BIGINT NOT NULL, admin_name VARCHAR(100) NOT NULL,
 operator_ip VARCHAR(64) NOT NULL, reason VARCHAR(255) NOT NULL, confirm_large BOOLEAN NOT NULL,
 application_updated_at DATETIME(3) NOT NULL, review_input JSON NOT NULL,
 created_at DATETIME(3) NOT NULL, updated_at DATETIME(3) NOT NULL,
 UNIQUE KEY uk_machine_refund_request(request_id), KEY idx_machine_refund_state(state)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE TABLE IF NOT EXISTS payment.machine_refund_legs (
 application_id BIGINT NOT NULL, instance_id BIGINT NOT NULL, customer_id BIGINT NOT NULL,
 ownership_version BIGINT NOT NULL, supplier_id BIGINT NOT NULL, host_id VARCHAR(64) NOT NULL,
 state VARCHAR(32) NOT NULL, remote_id VARCHAR(64) NOT NULL DEFAULT '', marker VARCHAR(128) NOT NULL,
 evidence JSON NOT NULL, updated_at DATETIME(3) NOT NULL,
 PRIMARY KEY(application_id,instance_id), KEY idx_machine_refund_host(instance_id,state)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS instance.instance_upgrades (
 id BIGINT NOT NULL PRIMARY KEY, order_id BIGINT NOT NULL, instance_id BIGINT NOT NULL, customer_id BIGINT NOT NULL,
 ownership_version BIGINT NOT NULL, supplier_id BIGINT NOT NULL, host_id VARCHAR(64) NOT NULL,
 source_product_id BIGINT NOT NULL, target_product_id BIGINT NOT NULL, remote_product_id VARCHAR(64) NOT NULL,
 target_name VARCHAR(255) NOT NULL, cycle VARCHAR(32) NOT NULL, amount DECIMAL(14,2) NOT NULL, cost DECIMAL(14,2) NOT NULL,
 request_id VARCHAR(64) NOT NULL, invoice_id VARCHAR(64) NOT NULL DEFAULT '', state VARCHAR(32) NOT NULL,
 expires_at DATETIME(3) NOT NULL, created_at DATETIME(3) NOT NULL, updated_at DATETIME(3) NOT NULL,
 UNIQUE KEY uk_upgrade_order(order_id), UNIQUE KEY uk_upgrade_request(request_id), KEY idx_upgrade_host(instance_id,state)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
