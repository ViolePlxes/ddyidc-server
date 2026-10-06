CREATE TABLE IF NOT EXISTS system.points_outbox (
 event_key VARCHAR(160) NOT NULL PRIMARY KEY,
 user_id BIGINT UNSIGNED NOT NULL,
 payload JSON NOT NULL,
 status ENUM('pending','done','retry') NOT NULL DEFAULT 'pending',
 attempts INT UNSIGNED NOT NULL DEFAULT 0,
 available_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 safe_error VARCHAR(200) NOT NULL DEFAULT '',
 created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 completed_at DATETIME(6) NULL,
 KEY idx_points_outbox_due (status,available_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
CREATE TABLE IF NOT EXISTS system.points_exchange_requests (
 user_id BIGINT UNSIGNED NOT NULL,
 request_id VARCHAR(80) NOT NULL,
 fingerprint CHAR(64) NOT NULL,
 order_id BIGINT UNSIGNED NULL,
 created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 PRIMARY KEY(user_id,request_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
CREATE TABLE IF NOT EXISTS system.points_admin_actions (
 admin_id BIGINT UNSIGNED NOT NULL,
 request_id VARCHAR(80) NOT NULL,
 fingerprint CHAR(64) NOT NULL,
 action VARCHAR(40) NOT NULL,
 notes VARCHAR(500) NOT NULL,
 operator_ip VARCHAR(64) NOT NULL,
 affected INT UNSIGNED NOT NULL,
 created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 PRIMARY KEY(admin_id,request_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
CREATE TABLE IF NOT EXISTS system.points_refund_events (
 event_key VARCHAR(160) NOT NULL PRIMARY KEY,
 user_id BIGINT UNSIGNED NOT NULL,
 event VARCHAR(40) NOT NULL,
 ref_id VARCHAR(80) NOT NULL,
 refund_amount DECIMAL(14,2) NOT NULL,
 paid_amount DECIMAL(14,2) NOT NULL,
 deducted BIGINT NOT NULL DEFAULT 0,
 created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 KEY idx_points_refund_user (user_id,created_at),
 KEY idx_points_refund_business (user_id,event,ref_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;


CREATE TABLE IF NOT EXISTS system.points_activities (
 id BIGINT UNSIGNED NOT NULL PRIMARY KEY,
 name VARCHAR(100) NOT NULL,
 description VARCHAR(1000) NOT NULL DEFAULT '',
 starts_at DATETIME NOT NULL,
 ends_at DATETIME NOT NULL,
 budget_points BIGINT UNSIGNED NOT NULL,
 awarded_points BIGINT UNSIGNED NOT NULL DEFAULT 0,
 rewards JSON NOT NULL,
 enabled TINYINT NOT NULL DEFAULT 0,
 updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
CREATE TABLE IF NOT EXISTS system.points_activity_claims (
 activity_id BIGINT UNSIGNED NOT NULL,
 user_id BIGINT UNSIGNED NOT NULL,
 period_key VARCHAR(8) NOT NULL,
 points BIGINT UNSIGNED NOT NULL,
 created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
 PRIMARY KEY(activity_id,user_id,period_key)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
