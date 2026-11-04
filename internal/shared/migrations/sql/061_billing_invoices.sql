-- Independent payable documents. Existing orders, payments and balance ledgers
-- remain intact. This migration never debits, credits or enqueues fulfillment.
CREATE TABLE IF NOT EXISTS billing.invoice_settings (
 id TINYINT PRIMARY KEY, enabled TINYINT NOT NULL DEFAULT 1,
 updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6)
);
INSERT IGNORE INTO billing.invoice_settings(id,enabled) VALUES(1,1);
CREATE TABLE IF NOT EXISTS billing.payable_invoices (
 id VARCHAR(72) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,
 invoice_no VARCHAR(160) NOT NULL, customer_id BIGINT UNSIGNED NOT NULL,
 source_type VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 source_id BIGINT UNSIGNED NOT NULL, revision INT UNSIGNED NOT NULL,
 source_no VARCHAR(128) NOT NULL, currency CHAR(3) NOT NULL DEFAULT 'CNY',
 amount_due DECIMAL(14,2) NOT NULL, amount_paid DECIMAL(14,2) NOT NULL DEFAULT 0,
 amount_refunded DECIMAL(14,2) NULL,
 status VARCHAR(24) NOT NULL, historical TINYINT NOT NULL DEFAULT 0,
 integrity VARCHAR(32) NOT NULL DEFAULT 'verified',
 created_at DATETIME(6) NOT NULL, paid_at DATETIME(6) NULL,
 voided_at DATETIME(6) NULL, updated_at DATETIME(6) NOT NULL,
 UNIQUE KEY uk_invoice_no(invoice_no), UNIQUE KEY uk_invoice_source_revision(source_type,source_id,revision),
 KEY idx_invoice_customer(customer_id,status,created_at,id),
 CHECK(amount_due>=0 AND amount_paid>=0 AND amount_paid<=amount_due),
 CHECK(amount_refunded IS NULL OR (amount_refunded>=0 AND amount_refunded<=amount_paid))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
CREATE TABLE IF NOT EXISTS billing.invoice_bindings (
 source_type VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 source_id BIGINT UNSIGNED NOT NULL,
 invoice_id VARCHAR(72) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 PRIMARY KEY(source_type,source_id), KEY idx_binding_invoice(invoice_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS billing.invoice_migration_checks (
 check_name VARCHAR(64) PRIMARY KEY, expected BIGINT NOT NULL, actual BIGINT NOT NULL,
 checked_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6), CHECK(expected=actual)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
CREATE TABLE IF NOT EXISTS billing.invoice_items (
 id VARCHAR(112) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,
 invoice_id VARCHAR(72) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 source_item_id BIGINT UNSIGNED NULL, description VARCHAR(512) NOT NULL,
 module_type VARCHAR(64) NOT NULL, quantity INT UNSIGNED NULL,
 unit_amount DECIMAL(14,2) NOT NULL, amount DECIMAL(14,2) NOT NULL,
 KEY idx_invoice_items(invoice_id,id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
CREATE TABLE IF NOT EXISTS billing.invoice_payment_links (
 payment_id BIGINT UNSIGNED PRIMARY KEY,
 invoice_id VARCHAR(72) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 amount DECIMAL(14,2) NOT NULL, currency CHAR(3) NOT NULL,
 created_at DATETIME(6) NOT NULL, KEY idx_invoice_payment(invoice_id,payment_id),
 CHECK(amount>0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
CREATE TABLE IF NOT EXISTS billing.invoice_events (
 id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
 invoice_id VARCHAR(72) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 event_key VARCHAR(190) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 event_type VARCHAR(40) NOT NULL, amount DECIMAL(14,2) NULL,
 reference_type VARCHAR(40) NOT NULL DEFAULT '', reference_id VARCHAR(128) NOT NULL DEFAULT '',
 created_at DATETIME(6) NOT NULL, UNIQUE KEY uk_invoice_event(event_key),
 KEY idx_invoice_event(invoice_id,id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
CREATE TABLE IF NOT EXISTS billing.invoice_unmatched_receipts (
 payment_id BIGINT UNSIGNED PRIMARY KEY, customer_id BIGINT UNSIGNED NOT NULL,
 amount DECIMAL(14,2) NOT NULL, currency CHAR(3) NOT NULL,
 reason VARCHAR(64) NOT NULL, created_at DATETIME(6) NOT NULL,
 KEY idx_invoice_unmatched_customer(customer_id,payment_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

