CREATE TABLE IF NOT EXISTS system.visit_events (
 id BIGINT NOT NULL AUTO_INCREMENT,
 occurred_at DATETIME(3) NOT NULL,
 request_id VARCHAR(64) NOT NULL DEFAULT '',
 host VARCHAR(128) NOT NULL DEFAULT '',
 method VARCHAR(8) NOT NULL DEFAULT '',
 path VARCHAR(512) NOT NULL DEFAULT '',
 route VARCHAR(128) NOT NULL DEFAULT '',
 referer_host VARCHAR(128) NOT NULL DEFAULT '',
 referer_path VARCHAR(512) NOT NULL DEFAULT '',
 page_title VARCHAR(128) NOT NULL DEFAULT '',
 identity_type VARCHAR(16) NOT NULL DEFAULT 'guest',
 identity_id BIGINT NULL,
 status_code SMALLINT NOT NULL DEFAULT 0,
 duration_ms INT NOT NULL DEFAULT 0,
 client_type VARCHAR(16) NOT NULL DEFAULT 'browser',
 browser VARCHAR(32) NOT NULL DEFAULT '',
 os VARCHAR(32) NOT NULL DEFAULT '',
 device VARCHAR(16) NOT NULL DEFAULT '',
 is_bot TINYINT NOT NULL DEFAULT 0,
 ip_hash CHAR(64) NOT NULL DEFAULT '',
 country VARCHAR(64) NOT NULL DEFAULT 'unknown',
 region VARCHAR(64) NOT NULL DEFAULT '',
 city VARCHAR(64) NOT NULL DEFAULT '',
 created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
 PRIMARY KEY (id, occurred_at),
 KEY visit_events_occurred (occurred_at),
 KEY visit_events_route (route, occurred_at),
 KEY visit_events_identity (identity_type, occurred_at),
 KEY visit_events_status (status_code, occurred_at),
 KEY visit_events_country (country, occurred_at),
 KEY visit_events_client (client_type, occurred_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci
PARTITION BY RANGE (TO_DAYS(occurred_at)) (
 PARTITION pmax VALUES LESS THAN MAXVALUE
);
CREATE TABLE IF NOT EXISTS system.visit_daily (
 day DATE NOT NULL,
 dimension VARCHAR(16) NOT NULL,
 dimension_value VARCHAR(384) NOT NULL DEFAULT '',
 requests BIGINT NOT NULL DEFAULT 0,
 visitors BIGINT NOT NULL DEFAULT 0,
 errors INT NOT NULL DEFAULT 0,
 updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
 PRIMARY KEY (day, dimension, dimension_value),
 KEY visit_daily_dimension (dimension, day)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
CREATE TABLE IF NOT EXISTS system.visit_analytics_audits (
 id BIGINT NOT NULL AUTO_INCREMENT,
 admin_id BIGINT NOT NULL,
 action VARCHAR(32) NOT NULL DEFAULT '',
 target VARCHAR(512) NOT NULL DEFAULT '',
 request_id VARCHAR(64) NOT NULL DEFAULT '',
 operator_ip VARCHAR(64) NOT NULL DEFAULT '',
 created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
 PRIMARY KEY (id),
 KEY visit_analytics_audit_admin (admin_id, created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
