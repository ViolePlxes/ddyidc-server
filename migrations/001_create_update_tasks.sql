-- 首次部署自动更新功能时由管理员人工执行；更新任务本身永不自动执行 DDL。
CREATE TABLE IF NOT EXISTS system.update_tasks (
    id bigint unsigned NOT NULL,
    target varchar(16) NOT NULL,
    status varchar(16) NOT NULL,
    progress int NOT NULL DEFAULT 0,
    trigger_by bigint unsigned NOT NULL,
    branch varchar(128) NOT NULL DEFAULT '',
    feature varchar(255) NOT NULL DEFAULT '',
    before_commit varchar(64) NOT NULL DEFAULT '',
    target_commit varchar(64) NOT NULL DEFAULT '',
    before_version varchar(64) NOT NULL DEFAULT '',
    target_version varchar(64) NOT NULL DEFAULT '',
    log mediumtext NOT NULL,
    error text NOT NULL,
    rollback_status varchar(255) NOT NULL DEFAULT '',
    created_at datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
    started_at datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
    finished_at datetime DEFAULT NULL,
    PRIMARY KEY (id),
    KEY idx_update_task_history (created_at),
    KEY idx_update_task_running (status, target)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='系统更新任务';
