-- Offline DBA grant for the auto-renewal lifecycle tables.
-- The runtime identity previously had column-level UPDATE on these two tables,
-- so the renewal claim (instance_product_lifecycle) and the auto quote pause
-- (instance_renewal_auto_quotes) failed with MySQL 1143/1142 and no renewal was
-- ever produced. Table-level DML only: no DDL, no balance/account access.
--
--   mysql --defaults-file=/etc/ddyidc/dba.cnf < grant-renewal-lifecycle-runtime.sql
GRANT SELECT, INSERT, UPDATE, DELETE ON instance.instance_product_lifecycle TO 'ddyidc_runtime'@'127.0.0.1';
GRANT SELECT, INSERT, UPDATE, DELETE ON instance.instance_renewal_auto_quotes TO 'ddyidc_runtime'@'127.0.0.1';
FLUSH PRIVILEGES;