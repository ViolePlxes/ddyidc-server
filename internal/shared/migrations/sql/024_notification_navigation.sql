INSERT INTO system.navigation_nodes
 (id,scope,parent_id,name,href,target,icon,gate,enabled,sort,created_at,updated_at)
SELECT 900759908150,'admin',parent_id,'通知任务','/admin/notification-tasks','self','BellIcon','',TRUE,2,UTC_TIMESTAMP(3),UTC_TIMESTAMP(3)
FROM system.navigation_nodes
WHERE scope='admin' AND href='/admin/system'
 AND NOT EXISTS (SELECT 1 FROM system.navigation_nodes existing WHERE existing.scope='admin' AND existing.href='/admin/notification-tasks')
LIMIT 1;
UPDATE system.navigation_scopes SET revision=revision+1 WHERE scope='admin' AND ROW_COUNT()>0;
