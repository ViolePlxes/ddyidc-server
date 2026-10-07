INSERT INTO system.navigation_nodes (id,scope,parent_id,name,href,target,icon,gate,enabled,sort,created_at,updated_at)
SELECT 7831060700331,'admin',parent_id,'福利活动','/admin/points/activities','self','GiftIcon','',1,7,NOW(),NOW()
FROM system.navigation_nodes
WHERE scope='admin' AND href='/admin/points'
AND NOT EXISTS(SELECT 1 FROM system.navigation_nodes WHERE scope='admin' AND href='/admin/points/activities')
ORDER BY id LIMIT 1;

INSERT INTO system.navigation_nodes (id,scope,parent_id,name,href,target,icon,gate,enabled,sort,created_at,updated_at)
SELECT 7831060700332,'admin',parent_id,'积分发放任务','/admin/points/event-tasks','self','ReceiptIcon','',1,8,NOW(),NOW()
FROM system.navigation_nodes
WHERE scope='admin' AND href='/admin/points'
AND NOT EXISTS(SELECT 1 FROM system.navigation_nodes WHERE scope='admin' AND href='/admin/points/event-tasks')
ORDER BY id LIMIT 1;

UPDATE system.navigation_scopes SET revision=revision+1 WHERE scope='admin';
