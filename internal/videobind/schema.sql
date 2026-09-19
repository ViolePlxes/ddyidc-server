CREATE TABLE IF NOT EXISTS system.video_platforms (
 code VARCHAR(24) PRIMARY KEY, name VARCHAR(40) NOT NULL, enabled BOOLEAN NOT NULL DEFAULT FALSE,
 mode VARCHAR(24) NOT NULL DEFAULT 'manual', credentials TEXT NOT NULL,
 callback_url VARCHAR(512) NOT NULL DEFAULT '', official_url VARCHAR(512) NOT NULL DEFAULT '',
 notes VARCHAR(512) NOT NULL DEFAULT '', updated_at DATETIME(3) NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE TABLE IF NOT EXISTS system.video_bindings (
 id BIGINT PRIMARY KEY, user_id BIGINT NOT NULL, platform VARCHAR(24) NOT NULL,
 identity_hash CHAR(64) NOT NULL, identity_cipher TEXT NOT NULL, nickname VARCHAR(100) NOT NULL DEFAULT '',
 status VARCHAR(24) NOT NULL DEFAULT 'unknown', verified BOOLEAN NOT NULL DEFAULT FALSE,
 awarded BOOLEAN NOT NULL DEFAULT FALSE, awarded_points BIGINT NOT NULL DEFAULT 0,
 last_event_at BIGINT NOT NULL DEFAULT 0, bound_at DATETIME(3) NOT NULL, followed_at DATETIME(3) NULL,
 unbound_at DATETIME(3) NULL, review_admin_id BIGINT NOT NULL DEFAULT 0, review_notes VARCHAR(512) NOT NULL DEFAULT '',
 updated_at DATETIME(3) NOT NULL,
 UNIQUE KEY uk_video_user_platform(user_id,platform), UNIQUE KEY uk_video_identity(platform,identity_hash)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
