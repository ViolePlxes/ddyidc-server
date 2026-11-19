-- Offline DBA grant for the finance execution ledger.
-- The table is created by migration 071_finance_execution_events.sql. Runtime
-- identities created by deploy/linux/database-identities.py receive full CRUD
-- automatically, but an install that already existed before the table needs this
-- explicit grant, otherwise every ledger write fails with MySQL 1142 and the
-- feature is silently blind.
--
--   mysql --defaults-file=/etc/ddyidc/dba.cnf < grant-finance-execution-runtime.sql
GRANT SELECT, INSERT, UPDATE, DELETE ON `order`.`execution_events` TO 'ddyidc_runtime'@'127.0.0.1';
FLUSH PRIVILEGES;
