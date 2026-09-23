-- Legacy imported rows with version/account 0 remain unchanged. Duplicate
-- modern versions or unfreezes fail migration; never repair by deleting audit.
SET @ddl = (SELECT IF(COUNT(*)=0,
 'CREATE UNIQUE INDEX uk_ledger_account_version ON billing.transactions ((NULLIF(account_id,0)),(NULLIF(account_version,0)))',
 'DO 0') FROM information_schema.STATISTICS WHERE TABLE_SCHEMA='billing' AND TABLE_NAME='transactions' AND INDEX_NAME='uk_ledger_account_version');
PREPARE ledger_ddl FROM @ddl;
EXECUTE ledger_ddl;
DEALLOCATE PREPARE ledger_ddl;
SET @ddl = (SELECT IF(COUNT(*)=0,
 'CREATE UNIQUE INDEX uk_ledger_unfreeze ON billing.transactions ((CASE WHEN type=''unfreeze'' AND related_type=''freeze'' THEN related_id ELSE NULL END))',
 'DO 0') FROM information_schema.STATISTICS WHERE TABLE_SCHEMA='billing' AND TABLE_NAME='transactions' AND INDEX_NAME='uk_ledger_unfreeze');
PREPARE ledger_ddl FROM @ddl;
EXECUTE ledger_ddl;
DEALLOCATE PREPARE ledger_ddl;
