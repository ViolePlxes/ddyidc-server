CREATE TABLE IF NOT EXISTS customer.auth_devices (
  id CHAR(36) PRIMARY KEY,
  user_id BIGINT NOT NULL,
  account VARCHAR(100) NOT NULL,
  auth_version BIGINT UNSIGNED NOT NULL,
  refresh_hash CHAR(64) NOT NULL,
  browser VARCHAR(512) NOT NULL,
  ip VARCHAR(64) NOT NULL,
  login_at DATETIME(6) NOT NULL,
  last_active_at DATETIME(6) NOT NULL,
  expires_at DATETIME(6) NOT NULL,
  UNIQUE KEY auth_devices_refresh (refresh_hash),
  KEY auth_devices_user (user_id, login_at)
) ENGINE=InnoDB;
