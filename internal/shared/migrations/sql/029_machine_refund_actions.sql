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
