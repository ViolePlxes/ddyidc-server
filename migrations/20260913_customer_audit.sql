CREATE TABLE IF NOT EXISTS customer.admin_customer_audits (
 id BIGINT UNSIGNED PRIMARY KEY,
 customer_id BIGINT UNSIGNED NOT NULL,
 admin_id BIGINT UNSIGNED NOT NULL,
 admin_name VARCHAR(128) NOT NULL,
 action VARCHAR(32) NOT NULL,
 fields TEXT NOT NULL,
 operator_ip VARCHAR(45) NOT NULL,
 created_at DATETIME NOT NULL,
 KEY idx_customer_time(customer_id,created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
