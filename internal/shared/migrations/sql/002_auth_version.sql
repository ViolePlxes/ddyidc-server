-- Apply with the migration account while application writes are stopped.
-- Requires MySQL >= 8.0.29. Runtime never executes this file.
UPDATE `system`.`system_configs` SET value='true' WHERE `key`='security.csrf_enabled';
CREATE TABLE IF NOT EXISTS `system`.`impersonation_request_audits` (
  request_id CHAR(36) NOT NULL PRIMARY KEY,
  actor_admin_id BIGINT NOT NULL,
  subject_customer_id BIGINT NOT NULL,
  method VARCHAR(16) NOT NULL,
  path VARCHAR(2048) NOT NULL,
  http_status INT NOT NULL DEFAULT 0,
  started_at DATETIME(6) NOT NULL,
  finished_at DATETIME(6) NULL,
  INDEX actor_started (actor_admin_id, started_at),
  INDEX subject_started (subject_customer_id, started_at)
) ENGINE=InnoDB;
SET @ddl = (SELECT IF(COUNT(*)=0, 'ALTER TABLE `customer`.`customers` ADD COLUMN auth_version BIGINT UNSIGNED NOT NULL DEFAULT 1', 'DO 0') FROM information_schema.COLUMNS WHERE TABLE_SCHEMA='customer' AND TABLE_NAME='customers' AND COLUMN_NAME='auth_version');
PREPARE version_ddl FROM @ddl;
EXECUTE version_ddl;
DEALLOCATE PREPARE version_ddl;
CREATE TRIGGER IF NOT EXISTS `customer`.`customers_auth_version` BEFORE UPDATE ON `customer`.`customers` FOR EACH ROW SET NEW.auth_version = IF(NOT (NEW.password_hash <=> OLD.password_hash) OR NOT (NEW.status <=> OLD.status) OR NOT (NEW.username <=> OLD.username) OR NOT (NEW.deleted_at <=> OLD.deleted_at), OLD.auth_version + 1, OLD.auth_version);
SET @ddl = (SELECT IF(COUNT(*)=0, 'ALTER TABLE `system`.`admins` ADD COLUMN auth_version BIGINT UNSIGNED NOT NULL DEFAULT 1', 'DO 0') FROM information_schema.COLUMNS WHERE TABLE_SCHEMA='system' AND TABLE_NAME='admins' AND COLUMN_NAME='auth_version');
PREPARE version_ddl FROM @ddl;
EXECUTE version_ddl;
DEALLOCATE PREPARE version_ddl;
CREATE TRIGGER IF NOT EXISTS `system`.`admins_auth_version` BEFORE UPDATE ON `system`.`admins` FOR EACH ROW SET NEW.auth_version = IF(NOT (NEW.password_hash <=> OLD.password_hash) OR NOT (NEW.status <=> OLD.status) OR NOT (NEW.username <=> OLD.username), OLD.auth_version + 1, OLD.auth_version);
SET @ddl = (SELECT IF(COUNT(*)=0, 'ALTER TABLE `system`.`sales_users` ADD COLUMN auth_version BIGINT UNSIGNED NOT NULL DEFAULT 1', 'DO 0') FROM information_schema.COLUMNS WHERE TABLE_SCHEMA='system' AND TABLE_NAME='sales_users' AND COLUMN_NAME='auth_version');
PREPARE version_ddl FROM @ddl;
EXECUTE version_ddl;
DEALLOCATE PREPARE version_ddl;
CREATE TRIGGER IF NOT EXISTS `system`.`sales_users_auth_version` BEFORE UPDATE ON `system`.`sales_users` FOR EACH ROW SET NEW.auth_version = IF(NOT (NEW.password_hash <=> OLD.password_hash) OR NOT (NEW.status <=> OLD.status) OR NOT (NEW.username <=> OLD.username), OLD.auth_version + 1, OLD.auth_version);
