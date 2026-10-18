-- Additive inbox schema. Runtime identities must not have DDL privileges.
CREATE TABLE IF NOT EXISTS system.notification_user_states (
 recipient_id BIGINT UNSIGNED PRIMARY KEY, revision BIGINT UNSIGNED NOT NULL DEFAULT 0, updated_at DATETIME(6) NOT NULL
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS system.notification_messages (
 inbox_seq BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY, id BIGINT UNSIGNED NOT NULL UNIQUE,
 recipient_id BIGINT UNSIGNED NOT NULL, type VARCHAR(24) NOT NULL, scene VARCHAR(64) NOT NULL,
 title VARCHAR(128) NOT NULL, summary VARCHAR(255) NOT NULL, content MEDIUMTEXT NOT NULL,
 business_type VARCHAR(32) NOT NULL DEFAULT '', business_id VARCHAR(64) NOT NULL DEFAULT '',
 source_event_key VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 source_occurred_at DATETIME(6) NOT NULL, created_revision BIGINT UNSIGNED NOT NULL,
 read_at DATETIME(6) NULL, deleted_at DATETIME(6) NULL, created_at DATETIME(6) NOT NULL,
 UNIQUE KEY recipient_event(recipient_id,source_event_key),
 KEY recipient_page(recipient_id,deleted_at,created_at,inbox_seq),
 KEY recipient_read(recipient_id,deleted_at,read_at,created_at,inbox_seq),
 KEY recipient_type(recipient_id,type,deleted_at,created_at,inbox_seq),
 KEY recipient_business(recipient_id,business_type,business_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE TABLE IF NOT EXISTS system.notification_changes (
 recipient_id BIGINT UNSIGNED NOT NULL, revision BIGINT UNSIGNED NOT NULL, kind VARCHAR(24) NOT NULL,
 message_id BIGINT UNSIGNED NULL, push_status VARCHAR(16) NOT NULL DEFAULT 'pending', created_at DATETIME(6) NOT NULL,
 PRIMARY KEY(recipient_id,revision), KEY dispatch_pending(push_status,created_at)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS system.notification_outbox (
 id BIGINT UNSIGNED PRIMARY KEY, event_key VARCHAR(191) CHARACTER SET ascii COLLATE ascii_bin NOT NULL UNIQUE,
 payload JSON NOT NULL, status VARCHAR(16) NOT NULL, attempts INT NOT NULL DEFAULT 0,
 next_attempt_at DATETIME(6) NOT NULL, last_error_code VARCHAR(64) NOT NULL DEFAULT '', created_at DATETIME(6) NOT NULL,
 KEY pending(status,next_attempt_at,id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS system.notification_preferences (
 recipient_id BIGINT UNSIGNED PRIMARY KEY, marketing_toast_enabled BOOLEAN NOT NULL DEFAULT TRUE, updated_at DATETIME(6) NOT NULL
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS system.notification_permissions (
 admin_id BIGINT UNSIGNED PRIMARY KEY, can_view BOOLEAN NOT NULL DEFAULT FALSE, can_create BOOLEAN NOT NULL DEFAULT FALSE,
 can_retry BOOLEAN NOT NULL DEFAULT FALSE, can_cancel BOOLEAN NOT NULL DEFAULT FALSE
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS system.notification_broadcasts (
 id BIGINT UNSIGNED PRIMARY KEY, request_id VARCHAR(96) NOT NULL UNIQUE, admin_id BIGINT UNSIGNED NOT NULL,
 reason VARCHAR(500) NOT NULL, operator_ip VARCHAR(64) NOT NULL, title VARCHAR(128) NOT NULL, summary VARCHAR(255) NOT NULL,
 content MEDIUMTEXT NOT NULL, audience_definition JSON NOT NULL, payload_hash CHAR(64) NOT NULL,
 status VARCHAR(24) NOT NULL, total INT NOT NULL DEFAULT 0, succeeded INT NOT NULL DEFAULT 0, failed INT NOT NULL DEFAULT 0,
 skipped INT NOT NULL DEFAULT 0, created_at DATETIME(6) NOT NULL, finished_at DATETIME(6) NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE TABLE IF NOT EXISTS system.notification_broadcast_targets (
 broadcast_id BIGINT UNSIGNED NOT NULL, recipient_id BIGINT UNSIGNED NOT NULL, status VARCHAR(24) NOT NULL,
 attempts INT NOT NULL DEFAULT 0, last_error_code VARCHAR(64) NOT NULL DEFAULT '', updated_at DATETIME(6) NOT NULL,
 PRIMARY KEY(broadcast_id,recipient_id), KEY pending(status,broadcast_id)
) ENGINE=InnoDB;
