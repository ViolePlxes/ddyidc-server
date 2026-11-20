-- Offline DBA grant for the finance renewal price reference table.
-- The table is created by migration 072_renewal_price_reference.sql; installs that
-- predate it must run this grant or the snapshot sync fails with MySQL 1142.
--
--   mysql --defaults-file=/etc/ddyidc/dba.cnf < grant-renewal-price-reference-runtime.sql
GRANT SELECT, INSERT, UPDATE, DELETE ON `instance`.`renewal_price_reference` TO 'ddyidc_runtime'@'127.0.0.1';
FLUSH PRIVILEGES;
