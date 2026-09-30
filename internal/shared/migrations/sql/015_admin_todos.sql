CREATE TABLE IF NOT EXISTS system.navigation_scopes (
 scope VARCHAR(16) PRIMARY KEY,
 revision BIGINT NOT NULL DEFAULT 0,
 seeded BOOLEAN NOT NULL DEFAULT FALSE
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS system.navigation_nodes (
 id BIGINT PRIMARY KEY,
 scope VARCHAR(16) NOT NULL,
 parent_id BIGINT NOT NULL DEFAULT 0,
 name VARCHAR(80) NOT NULL,
 href VARCHAR(2048) NOT NULL DEFAULT '',
 target VARCHAR(8) NOT NULL DEFAULT 'self',
 icon VARCHAR(80) NOT NULL DEFAULT 'FolderIcon',
 gate VARCHAR(2048) NOT NULL DEFAULT '',
 enabled BOOLEAN NOT NULL DEFAULT TRUE,
 sort INT NOT NULL DEFAULT 0,
 created_at DATETIME(3) NOT NULL,
 updated_at DATETIME(3) NOT NULL,
 INDEX navigation_level (scope,parent_id,sort,id)
) ENGINE=InnoDB;

-- Navigation is editor-owned data. This versioned upgrade runs once and never
-- resets later edits, disabling, sorting or deletion of the todo menu.
INSERT IGNORE INTO system.navigation_scopes(scope,revision,seeded) VALUES ('admin',0,0);
INSERT IGNORE INTO system.navigation_nodes(id,scope,parent_id,name,href,target,icon,gate,enabled,sort,created_at,updated_at)
SELECT 930000001,'admin',0,'待办事项','','self','ListChecksIcon','/admin/todos',1,COALESCE(MIN(sort),0)-1,NOW(),NOW() FROM (SELECT sort FROM system.navigation_nodes WHERE scope='admin' AND parent_id=0) n;
INSERT IGNORE INTO system.navigation_nodes(id,scope,parent_id,name,href,target,icon,gate,enabled,sort,created_at,updated_at) VALUES
(930000002,'admin',930000001,'待处理工单','/admin/todos/tickets','self','ListChecksIcon','/admin/todos/tickets',1,0,NOW(),NOW()),
(930000003,'admin',930000001,'待处理退款','/admin/todos/refunds','self','ListChecksIcon','/admin/todos/refunds',1,1,NOW(),NOW()),
(930000004,'admin',930000001,'待处理实名','/admin/todos/certifications','self','ListChecksIcon','/admin/todos/certifications',1,2,NOW(),NOW()),
(930000005,'admin',930000001,'待处理提现','/admin/todos/withdrawals','self','ListChecksIcon','/admin/todos/withdrawals',1,3,NOW(),NOW()),
(930000006,'admin',930000001,'待确认推介','/admin/todos/recommendations','self','ListChecksIcon','/admin/todos/recommendations',1,4,NOW(),NOW()),
(930000007,'admin',930000001,'开通中产品数量','/admin/todos/provisioning','self','ListChecksIcon','/admin/todos/provisioning',1,5,NOW(),NOW()),
(930000008,'admin',930000001,'待处理线下转账','/admin/todos/offline-transfers','self','ListChecksIcon','/admin/todos/offline-transfers',1,6,NOW(),NOW()),
(930000009,'admin',930000001,'待手动处理产品','/admin/todos/manual-products','self','ListChecksIcon','/admin/todos/manual-products',1,7,NOW(),NOW()),
(930000010,'admin',930000001,'即将到期产品','/admin/todos/expiring','self','ListChecksIcon','/admin/todos/expiring',1,8,NOW(),NOW());
UPDATE system.navigation_scopes SET revision=revision+1 WHERE scope='admin';
