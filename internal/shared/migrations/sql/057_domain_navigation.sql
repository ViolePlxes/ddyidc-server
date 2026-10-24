-- Add domain-service entry points once; preserve existing administrator customizations.

INSERT IGNORE INTO system.navigation_scopes(scope,revision,seeded) VALUES ('console',0,0);

INSERT INTO system.navigation_nodes (id,scope,parent_id,name,href,target,icon,gate,enabled,sort,created_at,updated_at)
SELECT 950000100,'console',0,'域名服务','','self','GlobeIcon','',1,1,NOW(),NOW()
WHERE EXISTS(SELECT 1 FROM system.navigation_scopes WHERE scope='console' AND seeded=1)
AND NOT EXISTS(SELECT 1 FROM system.navigation_nodes WHERE id=950000100 OR (scope='console' AND (name='域名服务' OR href='/console/domains' OR href LIKE '/console/domains/%')));

INSERT INTO system.navigation_nodes (id,scope,parent_id,name,href,target,icon,gate,enabled,sort,created_at,updated_at)
SELECT 950000101,'console',950000100,'域名总览','/console/domains','self','GlobeIcon','',1,0,NOW(),NOW()
WHERE EXISTS(SELECT 1 FROM system.navigation_nodes WHERE id=950000100 AND scope='console' AND parent_id=0 AND name='域名服务')
AND NOT EXISTS(SELECT 1 FROM system.navigation_nodes WHERE id=950000101 OR (scope='console' AND href='/console/domains'));

INSERT INTO system.navigation_nodes (id,scope,parent_id,name,href,target,icon,gate,enabled,sort,created_at,updated_at)
SELECT 950000102,'console',950000100,'注册与交易','/console/domains/purchase','self','GlobeIcon','',1,1,NOW(),NOW()
WHERE EXISTS(SELECT 1 FROM system.navigation_nodes WHERE id=950000100 AND scope='console' AND parent_id=0 AND name='域名服务')
AND NOT EXISTS(SELECT 1 FROM system.navigation_nodes WHERE id=950000102 OR (scope='console' AND href='/console/domains/purchase'));

INSERT INTO system.navigation_nodes (id,scope,parent_id,name,href,target,icon,gate,enabled,sort,created_at,updated_at)
SELECT 950000103,'console',950000100,'我的域名','/console/domains/list','self','GlobeIcon','',1,2,NOW(),NOW()
WHERE EXISTS(SELECT 1 FROM system.navigation_nodes WHERE id=950000100 AND scope='console' AND parent_id=0 AND name='域名服务')
AND NOT EXISTS(SELECT 1 FROM system.navigation_nodes WHERE id=950000103 OR (scope='console' AND href='/console/domains/list'));

INSERT INTO system.navigation_nodes (id,scope,parent_id,name,href,target,icon,gate,enabled,sort,created_at,updated_at)
SELECT 950000104,'console',950000100,'续费与赎回','/console/domains/billing','self','GlobeIcon','',1,1,NOW(),NOW()
WHERE EXISTS(SELECT 1 FROM system.navigation_nodes WHERE id=950000100 AND scope='console' AND parent_id=0 AND name='域名服务')
AND NOT EXISTS(SELECT 1 FROM system.navigation_nodes WHERE id=950000104 OR (scope='console' AND href='/console/domains/billing'));

INSERT INTO system.navigation_nodes (id,scope,parent_id,name,href,target,icon,gate,enabled,sort,created_at,updated_at)
SELECT 950000105,'console',950000100,'转入与过户','/console/domains/transfers','self','GlobeIcon','',1,4,NOW(),NOW()
WHERE EXISTS(SELECT 1 FROM system.navigation_nodes WHERE id=950000100 AND scope='console' AND parent_id=0 AND name='域名服务')
AND NOT EXISTS(SELECT 1 FROM system.navigation_nodes WHERE id=950000105 OR (scope='console' AND href='/console/domains/transfers'));

INSERT INTO system.navigation_nodes (id,scope,parent_id,name,href,target,icon,gate,enabled,sort,created_at,updated_at)
SELECT 950000106,'console',950000100,'信息模板','/console/domains/contacts','self','GlobeIcon','',1,5,NOW(),NOW()
WHERE EXISTS(SELECT 1 FROM system.navigation_nodes WHERE id=950000100 AND scope='console' AND parent_id=0 AND name='域名服务')
AND NOT EXISTS(SELECT 1 FROM system.navigation_nodes WHERE id=950000106 OR (scope='console' AND href='/console/domains/contacts'));

INSERT INTO system.navigation_nodes (id,scope,parent_id,name,href,target,icon,gate,enabled,sort,created_at,updated_at)
SELECT 950000107,'console',950000100,'DNS管理','/console/domains/dns','self','GlobeIcon','',1,6,NOW(),NOW()
WHERE EXISTS(SELECT 1 FROM system.navigation_nodes WHERE id=950000100 AND scope='console' AND parent_id=0 AND name='域名服务')
AND NOT EXISTS(SELECT 1 FROM system.navigation_nodes WHERE id=950000107 OR (scope='console' AND href='/console/domains/dns'));

INSERT INTO system.navigation_nodes (id,scope,parent_id,name,href,target,icon,gate,enabled,sort,created_at,updated_at)
SELECT 950000108,'console',950000100,'安全与工具','/console/domains/security','self','GlobeIcon','',1,7,NOW(),NOW()
WHERE EXISTS(SELECT 1 FROM system.navigation_nodes WHERE id=950000100 AND scope='console' AND parent_id=0 AND name='域名服务')
AND NOT EXISTS(SELECT 1 FROM system.navigation_nodes WHERE id=950000108 OR (scope='console' AND href='/console/domains/security'));

INSERT INTO system.navigation_nodes (id,scope,parent_id,name,href,target,icon,gate,enabled,sort,created_at,updated_at)
SELECT 950000109,'console',950000100,'任务与记录','/console/domains/tasks','self','GlobeIcon','',1,8,NOW(),NOW()
WHERE EXISTS(SELECT 1 FROM system.navigation_nodes WHERE id=950000100 AND scope='console' AND parent_id=0 AND name='域名服务')
AND NOT EXISTS(SELECT 1 FROM system.navigation_nodes WHERE id=950000109 OR (scope='console' AND href='/console/domains/tasks'));

UPDATE system.navigation_scopes SET revision=revision+1 WHERE scope='console' AND seeded=1;
