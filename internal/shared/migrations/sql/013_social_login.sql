CREATE TABLE IF NOT EXISTS system.social_login_configs (
  provider VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,
  app_id VARCHAR(128) NOT NULL DEFAULT '',
  secret_cipher TEXT NOT NULL,
  callback_url VARCHAR(1024) NOT NULL DEFAULT '',
  enabled BOOLEAN NOT NULL DEFAULT FALSE,
  revision BIGINT NOT NULL DEFAULT 0,
  updated_at DATETIME(6) NOT NULL
) ENGINE=InnoDB;
INSERT IGNORE INTO system.social_login_configs VALUES ('github','','','',FALSE,0,NOW(6)),('qq','','','',FALSE,0,NOW(6));
CREATE TABLE IF NOT EXISTS customer.social_bindings (
  id BIGINT PRIMARY KEY,
  user_id BIGINT NOT NULL,
  provider VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  app_id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  open_id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  nickname VARCHAR(64) NOT NULL,
  avatar VARCHAR(1024) NOT NULL,
  created_at DATETIME(6) NOT NULL,
  UNIQUE KEY social_identity (provider,app_id,open_id),
  UNIQUE KEY social_user_provider (user_id,provider)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS customer.social_registrations (
  user_id BIGINT PRIMARY KEY,
  initial_hash VARCHAR(255) NOT NULL,
  created_at DATETIME(6) NOT NULL
) ENGINE=InnoDB;
