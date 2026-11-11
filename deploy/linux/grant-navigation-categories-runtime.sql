-- Offline DBA grant for the navigation category table (migrations 069/070).
-- The runtime identity is granted per table by design: no DDL, no access to
-- other schemas. Run this after the migration creates the table, before the
-- navigation manager is used.
--
--   mysql --defaults-file=/etc/ddyidc/dba.cnf < grant-navigation-categories-runtime.sql
--
-- Both loopback principals are covered because the installer may create the
-- runtime identity on either one.
GRANT SELECT, INSERT, UPDATE, DELETE ON system.navigation_categories TO 'ddyidc_runtime'@'localhost';
GRANT SELECT, INSERT, UPDATE, DELETE ON system.navigation_categories TO 'ddyidc_runtime'@'127.0.0.1';
FLUSH PRIVILEGES;
