-- Customer console entry for machine destruction requests. The menu is
-- editor-owned data: insert only when the node is absent and never reorder the
-- administrator's existing layout.
INSERT IGNORE INTO system.navigation_nodes(id,scope,parent_id,name,href,target,icon,gate,enabled,sort,created_at,updated_at)
SELECT 940000001,'console',6,'实例销毁申请','/console/destroy','self','ArrowsClockwiseIcon','',1,
       COALESCE((SELECT MAX(sort) FROM system.navigation_nodes WHERE scope='console' AND parent_id=6),0)+1,
       NOW(),NOW()
WHERE EXISTS (SELECT 1 FROM system.navigation_nodes WHERE scope='console' AND parent_id=6)
  AND NOT EXISTS (SELECT 1 FROM system.navigation_nodes WHERE scope='console' AND href='/console/destroy');
UPDATE system.navigation_scopes SET revision=revision+1 WHERE scope='console';
