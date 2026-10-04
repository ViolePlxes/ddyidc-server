CREATE TABLE IF NOT EXISTS instance.admin_sync_tasks (
 id BIGINT NOT NULL PRIMARY KEY, admin_id BIGINT NOT NULL, request_id VARCHAR(64) NOT NULL,
 operator_ip VARCHAR(45) NOT NULL, state VARCHAR(32) NOT NULL, items JSON NOT NULL,
 created_at DATETIME(6) NOT NULL, updated_at DATETIME(6) NOT NULL,
 UNIQUE KEY uk_admin_request(admin_id,request_id), KEY idx_admin_created(admin_id,created_at)
);
CREATE TABLE IF NOT EXISTS instance.admin_instance_audits (
 id BIGINT NOT NULL PRIMARY KEY, admin_id BIGINT NOT NULL, operator_ip VARCHAR(45) NOT NULL,
 instance_id BIGINT NOT NULL, customer_id BIGINT NOT NULL, task_id BIGINT NOT NULL DEFAULT 0,
 action VARCHAR(32) NOT NULL, before_value JSON NOT NULL, after_value JSON NOT NULL,
 result VARCHAR(32) NOT NULL, created_at DATETIME(6) NOT NULL,
 KEY idx_instance_created(instance_id,created_at), KEY idx_task(task_id)
);
INSERT IGNORE INTO system.permissions(id,code,name,module) VALUES
 (947430628101,'instance:list','全局产品列表','instance'),
 (947430628102,'instance:export','产品列表导出','instance'),
 (947430628103,'instance:sync','实例只读批量同步','instance'),
 (947430628104,'instance:remark','全局实例备注编辑','instance'),
 (947430628105,'instance:contact','实例客户联系方式明文','instance');
INSERT INTO system.navigation_nodes(id,scope,parent_id,name,href,target,icon,gate,enabled,sort,created_at,updated_at)
SELECT 947430628106,'admin',COALESCE((SELECT id FROM (SELECT id FROM system.navigation_nodes WHERE scope='admin' AND parent_id=0 AND name='概览' ORDER BY id LIMIT 1) AS parent),0),'产品列表','/admin/instances','self','PackageIcon','',TRUE,3,NOW(3),NOW(3)
WHERE NOT EXISTS(SELECT 1 FROM system.navigation_nodes WHERE scope='admin' AND href='/admin/instances');
UPDATE system.navigation_scopes SET revision=revision+1 WHERE scope='admin' AND ROW_COUNT()>0;
