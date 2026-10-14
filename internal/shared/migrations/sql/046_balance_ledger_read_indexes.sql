-- Non-destructive indexes for locked ledger scans and stable customer statements.
SET @ddl=(SELECT IF(COUNT(*)=0,'CREATE INDEX idx_customer_ledger_version ON billing.transactions (customer_id,account_version,id)','DO 0') FROM information_schema.STATISTICS WHERE TABLE_SCHEMA='billing' AND TABLE_NAME='transactions' AND INDEX_NAME='idx_customer_ledger_version');
PREPARE ledger_read_ddl FROM @ddl;
EXECUTE ledger_read_ddl;
DEALLOCATE PREPARE ledger_read_ddl;
SET @ddl=(SELECT IF(COUNT(*)=0,'CREATE INDEX idx_customer_statement_time ON billing.transactions (customer_id,created_at,account_version,id)','DO 0') FROM information_schema.STATISTICS WHERE TABLE_SCHEMA='billing' AND TABLE_NAME='transactions' AND INDEX_NAME='idx_customer_statement_time');
PREPARE ledger_read_ddl FROM @ddl;
EXECUTE ledger_read_ddl;
DEALLOCATE PREPARE ledger_read_ddl;
