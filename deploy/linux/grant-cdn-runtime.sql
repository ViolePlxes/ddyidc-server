-- Offline DBA after 040; existing runtime identity. Never grant DDL or schema-wide DML.
GRANT SELECT,INSERT,UPDATE ON system.cdnsell_usage_windows TO 'ddyidc_runtime'@'127.0.0.1';
GRANT SELECT,INSERT,UPDATE ON system.cdnsell_usage_allocations TO 'ddyidc_runtime'@'127.0.0.1';
GRANT SELECT,INSERT,UPDATE ON system.cdnsell_usage_cursors TO 'ddyidc_runtime'@'127.0.0.1';
GRANT SELECT,INSERT ON system.cdnsell_admin_audits TO 'ddyidc_runtime'@'127.0.0.1';
