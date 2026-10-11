ALTER TABLE system.cdnsell_domains
 ADD COLUMN auto_suspended TINYINT(1) NOT NULL DEFAULT 0,
 ADD COLUMN manual_disabled TINYINT(1) NOT NULL DEFAULT 0,
 ADD COLUMN auto_task_id BIGINT UNSIGNED NOT NULL DEFAULT 0;
UPDATE system.cdnsell_domains SET manual_disabled=1 WHERE enabled=0;
