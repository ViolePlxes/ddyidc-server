CREATE TABLE IF NOT EXISTS instance.instance_renewal_quote_evidence (
 id BIGINT NOT NULL PRIMARY KEY,
 renewal_id BIGINT NOT NULL, order_id BIGINT NOT NULL, customer_id BIGINT NOT NULL,
 instance_id BIGINT NOT NULL, ownership_version BIGINT NOT NULL,
 base_expiry DATETIME(3) NOT NULL, supplier_id BIGINT NOT NULL, remote_host_id VARCHAR(64) NOT NULL,
 product_id BIGINT NOT NULL, config_fingerprint CHAR(64) NOT NULL,
 cycle VARCHAR(32) NOT NULL, duration BIGINT NOT NULL,
 currency VARCHAR(8) NOT NULL, retail_amount DECIMAL(14,2) NOT NULL,
 procurement_amount DECIMAL(14,2) NOT NULL, exchange_rate VARCHAR(64) NOT NULL,
 source_kind VARCHAR(32) NOT NULL, source_version VARCHAR(64) NOT NULL,
 revision BIGINT NOT NULL, fingerprint CHAR(64) NOT NULL,
 quoted_at DATETIME(3) NOT NULL, expires_at DATETIME(3) NOT NULL,
 confirmed_at DATETIME(3) NOT NULL, created_at DATETIME(3) NOT NULL,
 UNIQUE KEY uq_renewal_quote_revision (order_id,revision),
 KEY idx_renewal_quote_owner (customer_id,order_id,expires_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS instance.instance_renewal_payment_quotes (
 payment_id BIGINT NOT NULL PRIMARY KEY, evidence_id BIGINT NOT NULL,
 order_id BIGINT NOT NULL, customer_id BIGINT NOT NULL, amount_fen BIGINT NOT NULL,
 created_at DATETIME(3) NOT NULL,
 KEY idx_renewal_payment_quote_order (order_id,payment_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS instance.instance_renewal_auto_quotes (
 instance_id BIGINT NOT NULL PRIMARY KEY, customer_id BIGINT NOT NULL,
 ownership_version BIGINT NOT NULL, quote_revision CHAR(64) NOT NULL,
 amount DECIMAL(14,2) NOT NULL, cycle VARCHAR(32) NOT NULL,
 updated_at DATETIME(3) NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE TABLE IF NOT EXISTS instance.instance_renewal_quote_alerts (
 id BIGINT NOT NULL PRIMARY KEY, customer_id BIGINT NOT NULL,
 instance_id BIGINT NOT NULL, fingerprint CHAR(64) NOT NULL,
 reason VARCHAR(64) NOT NULL, old_amount VARCHAR(32) NOT NULL,
 new_amount VARCHAR(32) NOT NULL, state VARCHAR(16) NOT NULL,
 created_at DATETIME(3) NOT NULL,
 UNIQUE KEY uq_renewal_quote_alert (instance_id,customer_id,fingerprint),
 KEY idx_renewal_quote_alert_owner (customer_id,created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
