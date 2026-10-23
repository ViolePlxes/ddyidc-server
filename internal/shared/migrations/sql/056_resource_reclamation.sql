CREATE TABLE IF NOT EXISTS system.reclamation_state (
 id TINYINT PRIMARY KEY, status VARCHAR(24) NOT NULL DEFAULT 'pending',
 heartbeat_at DATETIME(6) NULL, requested_at DATETIME(6) NULL, requested_by BIGINT NOT NULL DEFAULT 0, started_at DATETIME(6) NULL, finished_at DATETIME(6) NULL,
 attempts INT NOT NULL DEFAULT 0, next_attempt_at DATETIME(6) NULL,
 last_error VARCHAR(255) NOT NULL DEFAULT '', summary JSON NULL, preview JSON NULL
);
INSERT IGNORE INTO system.reclamation_state(id,status) VALUES(1,'pending');
CREATE TABLE IF NOT EXISTS system.reclamation_resources (
 resource_key VARCHAR(190) PRIMARY KEY, task_id BIGINT NOT NULL,
 category VARCHAR(40) NOT NULL, relative_path VARCHAR(512) NOT NULL, status VARCHAR(24) NOT NULL,
 attempts INT NOT NULL DEFAULT 0, allocated_bytes BIGINT UNSIGNED NOT NULL DEFAULT 0,
 last_error VARCHAR(255) NOT NULL DEFAULT '', updated_at DATETIME(6) NOT NULL,
 INDEX idx_reclamation_task(task_id)
);
CREATE TABLE IF NOT EXISTS system.reclamation_runs (
 id BIGINT PRIMARY KEY, status VARCHAR(24) NOT NULL, dry_run TINYINT NOT NULL,
 requested_by BIGINT NOT NULL DEFAULT 0, started_at DATETIME(6) NOT NULL,
 finished_at DATETIME(6) NULL, summary JSON NULL, last_error VARCHAR(255) NOT NULL DEFAULT '',
 INDEX idx_reclamation_started(started_at,id)
);
