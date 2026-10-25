SET @code_col = (SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA='marketing' AND TABLE_NAME='invites' AND COLUMN_NAME='pending_code');
SET @code_ddl = IF(@code_col=0,'ALTER TABLE marketing.invites ADD COLUMN pending_code VARCHAR(12) NULL DEFAULT NULL','SELECT 1');
PREPARE code_stmt FROM @code_ddl; EXECUTE code_stmt; DEALLOCATE PREPARE code_stmt;
CREATE TABLE IF NOT EXISTS marketing.code_policy (
 id TINYINT PRIMARY KEY,payload JSON NOT NULL,version BIGINT NOT NULL DEFAULT 1,scan_cursor BIGINT NOT NULL DEFAULT 0,updated_at DATETIME(3) NOT NULL
);
INSERT IGNORE INTO marketing.code_policy VALUES(1,JSON_OBJECT('blockedTerms',JSON_ARRAY(),'reservedCodes',JSON_ARRAY()),1,0,NOW(3));
CREATE TABLE IF NOT EXISTS marketing.code_claims (
 code VARCHAR(16) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci PRIMARY KEY,owner_id BIGINT NOT NULL,kind VARCHAR(16) NOT NULL,created_at DATETIME(3) NOT NULL
);
INSERT IGNORE INTO marketing.code_claims(code,owner_id,kind,created_at) SELECT UPPER(code),user_id,'issued',created_at FROM marketing.invites;
CREATE TABLE IF NOT EXISTS marketing.code_audits (
 id BIGINT PRIMARY KEY,admin_id BIGINT NOT NULL,operator_ip VARCHAR(45) NOT NULL,action VARCHAR(24) NOT NULL,user_id BIGINT NOT NULL,old_code VARCHAR(16) NOT NULL,new_code VARCHAR(16) NOT NULL,notes VARCHAR(1000) NOT NULL,created_at DATETIME(3) NOT NULL,KEY code_audit_created(created_at,id)
);
UPDATE system.system_configs SET value='1' WHERE `key`='marketing.invite.review_enabled';
