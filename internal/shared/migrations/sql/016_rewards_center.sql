ALTER TABLE system.points_settings ADD COLUMN signin_progressive BOOLEAN NOT NULL DEFAULT FALSE AFTER signin_mode,
ADD COLUMN checkout_enabled BOOLEAN NOT NULL DEFAULT TRUE,
ADD COLUMN checkout_points_per_yuan BIGINT NOT NULL DEFAULT 1000,
ADD COLUMN checkout_coupon_days INT NOT NULL DEFAULT 30;
CREATE TABLE IF NOT EXISTS system.points_checkout_coupons (
 user_id BIGINT UNSIGNED NOT NULL,request_id VARCHAR(64) NOT NULL,points BIGINT NOT NULL,
 user_coupon_id BIGINT UNSIGNED NOT NULL,amount_yuan DECIMAL(14,2) NOT NULL,
 PRIMARY KEY(user_id,request_id),UNIQUE KEY points_checkout_coupon(user_coupon_id)
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS system.points_events (
 user_id BIGINT UNSIGNED NOT NULL,event VARCHAR(40) NOT NULL,ref_id VARCHAR(100) NOT NULL,
 created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
 PRIMARY KEY(user_id,event,ref_id)
) ENGINE=InnoDB;
INSERT IGNORE INTO system.points_settings(id,enabled,signin_progressive,streak_milestones)
VALUES(1,0,1,'[{"days":1,"points":0},{"days":2,"points":1},{"days":3,"points":2},{"days":4,"points":3},{"days":5,"points":4},{"days":6,"points":5},{"days":7,"points":10}]');
-- Preserve operator settings; upgrade only the untouched original reward schedule.
UPDATE system.points_settings SET signin_progressive=1,streak_milestones='[{"days":1,"points":0},{"days":2,"points":1},{"days":3,"points":2},{"days":4,"points":3},{"days":5,"points":4},{"days":6,"points":5},{"days":7,"points":10}]'
WHERE signin_mode='normal' AND signin_stable_points=5 AND JSON_LENGTH(streak_milestones)=4
AND streak_reward_enabled=1 AND streak_max_mode='keep' AND streak_reset_on_break=1
AND JSON_CONTAINS(streak_milestones,'[{"days":3,"points":10},{"days":7,"points":30},{"days":15,"points":60},{"days":30,"points":150}]');
INSERT IGNORE INTO system.points_tasks(id,name,code,description,target_event,task_type,category,points,target_value,enabled,sort) VALUES
(940001001,'绑定手机号','reward_bind_phone','绑定手机号后可领取一次奖励','bind_phone','once','newbie',100,1,1,100),
(940001002,'实名认证','reward_certification','完成平台实名认证后可领取一次奖励','certification','once','newbie',200,1,1,99),
(940001003,'完善个人资料','reward_profile','填写昵称和邮箱后可领取一次奖励','profile_complete','once','newbie',50,1,1,98),
(940001004,'每日登录','reward_login','登录账户，每日领取一次','login','daily','daily',5,1,1,90),
(940001005,'访问控制台','reward_visit','访问用户控制台，每日领取一次','console_visit','daily','daily',5,1,1,89);
INSERT IGNORE INTO system.points_rules(id,name,code,event,type,points_fixed,ratio,ladder_config,limit_type,limit_value,enabled) VALUES
(940002001,'充值返积分','reward_recharge','recharge','ratio',0,100,'[]','none',0,1),
(940002002,'消费返积分','reward_order','order','ratio',0,100,'[]','none',0,1),
(940002003,'邀请好友注册','reward_invite','invite','fixed',100,0,'[]','none',0,1),
(940002004,'好友首充奖励','reward_invite_first_recharge','invite_first_recharge','fixed',200,0,'[]','none',0,1),
(940002005,'好友消费返积分','reward_invite_order','invite_order','ratio',0,100,'[]','none',0,1);
-- Menus remain persisted, editable navigation data, seeded once by this upgrade.
INSERT IGNORE INTO system.navigation_nodes(id,scope,parent_id,name,href,target,icon,gate,enabled,sort,created_at,updated_at)
SELECT 940000001,'admin',0,'积分中心','','self','CoinsIcon','',1,20,NOW(),NOW()
WHERE EXISTS(SELECT 1 FROM system.navigation_scopes WHERE scope='admin' AND seeded=1) AND NOT EXISTS(SELECT 1 FROM system.navigation_nodes WHERE scope='admin' AND href='/admin/points');
SET @reward_admin_group=COALESCE((SELECT parent_id FROM system.navigation_nodes WHERE scope='admin' AND href='/admin/points' ORDER BY id LIMIT 1),IF(EXISTS(SELECT 1 FROM system.navigation_scopes WHERE scope='admin' AND seeded=1),940000001,66));
INSERT IGNORE INTO system.navigation_nodes(id,scope,parent_id,name,href,target,icon,gate,enabled,sort,created_at,updated_at)
SELECT 940000002,'admin',@reward_admin_group,'优惠券管理','/admin/points/coupons','self','GiftIcon','',1,6,NOW(),NOW()
WHERE NOT EXISTS(SELECT 1 FROM system.navigation_nodes WHERE scope='admin' AND href='/admin/points/coupons');
INSERT IGNORE INTO system.navigation_nodes(id,scope,parent_id,name,href,target,icon,gate,enabled,sort,created_at,updated_at)
SELECT 940000003,'admin',@reward_admin_group,'签到与任务配置','/admin/points/plugin','self','GearIcon','',1,5,NOW(),NOW()
WHERE NOT EXISTS(SELECT 1 FROM system.navigation_nodes WHERE scope='admin' AND href='/admin/points/plugin');
UPDATE system.navigation_scopes SET revision=revision+1 WHERE scope='admin';
