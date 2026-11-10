-- Sidebar category ("大类") is editor-owned data now: the six product areas the
-- sidebar groups sections under. Existing groups are backfilled by name so the
-- menu keeps its current shape after the upgrade.
ALTER TABLE system.navigation_nodes ADD COLUMN category VARCHAR(32) NOT NULL DEFAULT '' AFTER gate;

UPDATE system.navigation_nodes SET category='业务管理' WHERE parent_id=0 AND name IN ('待办事项','概览','商品管理','上下游');
UPDATE system.navigation_nodes SET category='服务管理' WHERE parent_id=0 AND name IN ('域名服务','域名服务管理','产品与服务','资源与账单','CDN 加速','CDN加速');
UPDATE system.navigation_nodes SET category='支持与设置' WHERE parent_id=0 AND name IN ('技术支持','客户服务','系统设置');
UPDATE system.navigation_nodes SET category='账户与权益' WHERE parent_id=0 AND name IN ('账户','积分中心','福利中心');
UPDATE system.navigation_nodes SET category='运营管理' WHERE parent_id=0 AND name IN ('营销中心','交易市场');
UPDATE system.navigation_nodes SET category='销售管理' WHERE parent_id=0 AND name IN ('销售中心','授权客户');

UPDATE system.navigation_scopes SET revision=revision+1;
