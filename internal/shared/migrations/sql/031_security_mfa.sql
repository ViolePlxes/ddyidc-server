CREATE TABLE IF NOT EXISTS system.mfa_factors (
 subject_type VARCHAR(16) NOT NULL, subject_id BIGINT NOT NULL,
 secret_cipher TEXT NOT NULL, enabled BOOLEAN NOT NULL DEFAULT FALSE,
 pending_cipher TEXT NOT NULL, pending_expires_at DATETIME(3) NULL,
 recovery_hashes JSON NOT NULL, last_step BIGINT NOT NULL DEFAULT -1, version BIGINT NOT NULL DEFAULT 0,
 created_at DATETIME(3) NOT NULL, updated_at DATETIME(3) NOT NULL,
 PRIMARY KEY(subject_type,subject_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE TABLE IF NOT EXISTS system.mfa_challenges (
 token_hash CHAR(64) NOT NULL PRIMARY KEY, subject_type VARCHAR(16) NOT NULL,
 subject_id BIGINT NOT NULL, auth_version BIGINT UNSIGNED NOT NULL,
 enroll BOOLEAN NOT NULL DEFAULT FALSE, ip VARCHAR(45) NOT NULL,
 browser_hash CHAR(64) NOT NULL, attempts INT NOT NULL DEFAULT 0,
 policy_version BIGINT NOT NULL, factor_version BIGINT NOT NULL DEFAULT 0, expires_at DATETIME(3) NOT NULL,
 used_at DATETIME(3) NULL, KEY idx_mfa_expiry(expires_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
INSERT IGNORE INTO system.system_configs(id,`key`,value,description)
VALUES (783106070031,'security.admin_access',JSON_OBJECT('adminPath',CONCAT('/dd-admin-',LOWER(HEX(RANDOM_BYTES(12)))),'mfaEnabled',FALSE,'issuer','DDYIDC','version',1),'后台随机入口与MFA策略');
