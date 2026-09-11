CREATE TABLE IF NOT EXISTS `system`.`points_settings` (
  `id` bigint unsigned NOT NULL,
  `enabled` tinyint(1) NOT NULL DEFAULT 0,
  `points_expire_days` int unsigned NOT NULL DEFAULT 0,
  `show_mall` tinyint(1) NOT NULL DEFAULT 1,
  `show_signin` tinyint(1) NOT NULL DEFAULT 1,
  `show_tasks` tinyint(1) NOT NULL DEFAULT 1,
  `refund_mode` enum('none','full','ratio') NOT NULL DEFAULT 'full',
  `refund_insufficient` enum('cut_to_zero','none') NOT NULL DEFAULT 'cut_to_zero',
  `recharge_refund_deduct` tinyint(1) NOT NULL DEFAULT 1,
  `signin_mode` enum('normal','choose') NOT NULL DEFAULT 'normal',
  `signin_stable_points` int unsigned NOT NULL DEFAULT 5,
  `signin_lucky_min` int unsigned NOT NULL DEFAULT 1,
  `signin_lucky_max` int unsigned NOT NULL DEFAULT 20,
  `streak_reward_enabled` tinyint(1) NOT NULL DEFAULT 1,
  `streak_reset_on_break` tinyint(1) NOT NULL DEFAULT 1,
  `streak_max_mode` enum('keep','reset') NOT NULL DEFAULT 'keep',
  `streak_milestones` json NOT NULL,
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='积分插件设置';

CREATE TABLE IF NOT EXISTS `system`.`points_rules` (
  `id` bigint unsigned NOT NULL, `name` varchar(100) NOT NULL, `code` varchar(64) NOT NULL,
  `event` varchar(40) NOT NULL, `type` enum('fixed','random','ratio','ladder') NOT NULL DEFAULT 'fixed',
  `points_fixed` int NOT NULL DEFAULT 0, `points_min` int NOT NULL DEFAULT 0, `points_max` int NOT NULL DEFAULT 0,
  `ratio` decimal(8,4) NOT NULL DEFAULT 0, `ladder_config` json NOT NULL,
  `limit_type` enum('none','once','day','week','month') NOT NULL DEFAULT 'none', `limit_value` int unsigned NOT NULL DEFAULT 0,
  `enabled` tinyint(1) NOT NULL DEFAULT 1, `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`), UNIQUE KEY `uk_points_rule_code` (`code`), KEY `idx_points_rule_event` (`event`,`enabled`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='积分规则';

CREATE TABLE IF NOT EXISTS `system`.`points_tasks` (
  `id` bigint unsigned NOT NULL, `name` varchar(100) NOT NULL, `code` varchar(64) NOT NULL,
  `description` varchar(255) NOT NULL DEFAULT '', `target_event` varchar(40) NOT NULL,
  `task_type` enum('once','daily','weekly','monthly') NOT NULL DEFAULT 'once',
  `category` enum('newbie','daily','growth') NOT NULL DEFAULT 'daily',
  `points` int unsigned NOT NULL, `target_value` int unsigned NOT NULL DEFAULT 1,
  `enabled` tinyint(1) NOT NULL DEFAULT 1, `sort` int NOT NULL DEFAULT 0,
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP, `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`), UNIQUE KEY `uk_points_task_code` (`code`), KEY `idx_points_task_event` (`target_event`,`enabled`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='积分任务';

CREATE TABLE IF NOT EXISTS `system`.`points_task_progress` (
  `user_id` bigint unsigned NOT NULL, `task_id` bigint unsigned NOT NULL, `period_key` varchar(20) NOT NULL,
  `progress` int unsigned NOT NULL DEFAULT 0, `claimed_at` datetime DEFAULT NULL, `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`user_id`,`task_id`,`period_key`), KEY `idx_points_task_progress_task` (`task_id`,`period_key`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='积分任务进度';

CREATE TABLE IF NOT EXISTS `system`.`points_accounts` (
  `user_id` bigint unsigned NOT NULL, `balance` bigint unsigned NOT NULL DEFAULT 0, `frozen` bigint unsigned NOT NULL DEFAULT 0,
  `total_earned` bigint unsigned NOT NULL DEFAULT 0, `total_used` bigint unsigned NOT NULL DEFAULT 0,
  `status` tinyint(1) NOT NULL DEFAULT 1, `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`user_id`), KEY `idx_points_account_status` (`status`,`balance`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='用户积分账户';

CREATE TABLE IF NOT EXISTS `system`.`points_records` (
  `id` bigint unsigned NOT NULL, `user_id` bigint unsigned NOT NULL, `type` enum('gain','deduct') NOT NULL,
  `source` varchar(40) NOT NULL, `event` varchar(40) NOT NULL DEFAULT '', `change_value` bigint NOT NULL,
  `balance_after` bigint unsigned NOT NULL, `expire_at` datetime DEFAULT NULL, `ref_id` varchar(80) NOT NULL DEFAULT '',
  `remark` varchar(255) NOT NULL DEFAULT '', `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`), UNIQUE KEY `uk_points_record_idempotent` (`user_id`,`event`,`ref_id`,`source`),
  KEY `idx_points_record_user` (`user_id`,`created_at`), KEY `idx_points_record_expire` (`expire_at`,`type`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='积分流水';

CREATE TABLE IF NOT EXISTS `system`.`points_signins` (
  `id` bigint unsigned NOT NULL, `user_id` bigint unsigned NOT NULL, `signin_date` date NOT NULL,
  `mode` enum('normal','stable','lucky') NOT NULL, `points` int unsigned NOT NULL, `streak` int unsigned NOT NULL,
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`), UNIQUE KEY `uk_points_signin_day` (`user_id`,`signin_date`), KEY `idx_points_signin_user` (`user_id`,`signin_date`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='签到记录';

CREATE TABLE IF NOT EXISTS `system`.`points_goods_categories` (
  `id` bigint unsigned NOT NULL, `parent_id` bigint unsigned NOT NULL DEFAULT 0, `name` varchar(100) NOT NULL, `sort` int NOT NULL DEFAULT 0,
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP, `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`), KEY `idx_points_category_parent` (`parent_id`,`sort`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='积分商品分类';

CREATE TABLE IF NOT EXISTS `system`.`points_goods` (
  `id` bigint unsigned NOT NULL, `category_id` bigint unsigned NOT NULL DEFAULT 0, `name` varchar(120) NOT NULL,
  `type` enum('virtual','physical','coupon','balance','system','card') NOT NULL, `image` varchar(500) NOT NULL DEFAULT '',
  `description` text NOT NULL, `points_cost` int unsigned NOT NULL, `stock` int NOT NULL DEFAULT -1, `sold` int unsigned NOT NULL DEFAULT 0,
  `status` tinyint(1) NOT NULL DEFAULT 1, `config` json NOT NULL, `is_hot` tinyint(1) NOT NULL DEFAULT 0, `sort` int NOT NULL DEFAULT 0,
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP, `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`), KEY `idx_points_goods_public` (`status`,`category_id`,`sort`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='积分商品';

CREATE TABLE IF NOT EXISTS `system`.`points_card_batches` (
  `id` bigint unsigned NOT NULL, `goods_id` bigint unsigned NOT NULL, `total` int unsigned NOT NULL DEFAULT 0, `used` int unsigned NOT NULL DEFAULT 0,
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP, PRIMARY KEY (`id`), KEY `idx_points_card_batch_goods` (`goods_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='卡密批次';

CREATE TABLE IF NOT EXISTS `system`.`points_cards` (
  `id` bigint unsigned NOT NULL, `batch_id` bigint unsigned NOT NULL, `goods_id` bigint unsigned NOT NULL,
  `card_value` varchar(1000) NOT NULL, `used` tinyint(1) NOT NULL DEFAULT 0, `used_by` bigint unsigned DEFAULT NULL, `used_at` datetime DEFAULT NULL,
  PRIMARY KEY (`id`), UNIQUE KEY `uk_points_card_value` (`goods_id`,`card_value`(190)), KEY `idx_points_card_available` (`goods_id`,`used`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='卡密池';

CREATE TABLE IF NOT EXISTS `system`.`points_orders` (
  `id` bigint unsigned NOT NULL, `order_no` varchar(32) NOT NULL, `user_id` bigint unsigned NOT NULL, `goods_id` bigint unsigned NOT NULL,
  `goods_name` varchar(120) NOT NULL, `goods_type` varchar(20) NOT NULL, `points_cost` int unsigned NOT NULL, `quantity` int unsigned NOT NULL DEFAULT 1,
  `status` enum('pending','processing','completed','cancelled') NOT NULL DEFAULT 'pending', `recipient` json NOT NULL,
  `tracking` json NOT NULL, `config_snapshot` json NOT NULL, `delivery_content` text NOT NULL,
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP, `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`), UNIQUE KEY `uk_points_order_no` (`order_no`), KEY `idx_points_order_user` (`user_id`,`status`,`created_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='积分兑换订单';

CREATE TABLE IF NOT EXISTS `system`.`points_risk_tags` (
  `id` bigint unsigned NOT NULL, `name` varchar(64) NOT NULL, `level` tinyint unsigned NOT NULL, `color` varchar(32) NOT NULL DEFAULT '',
  `description` varchar(255) NOT NULL DEFAULT '', `is_locked_default` tinyint(1) NOT NULL DEFAULT 0,
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP, `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`), KEY `idx_points_risk_level` (`level`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='积分风控标签';

CREATE TABLE IF NOT EXISTS `system`.`points_risk_rules` (
  `id` bigint unsigned NOT NULL, `name` varchar(100) NOT NULL, `tag_id` bigint unsigned NOT NULL, `conditions` json NOT NULL,
  `enabled` tinyint(1) NOT NULL DEFAULT 1, `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`), KEY `idx_points_risk_rule_enabled` (`enabled`,`tag_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='积分风控规则';

CREATE TABLE IF NOT EXISTS `system`.`points_user_tags` (
  `user_id` bigint unsigned NOT NULL, `tag_id` bigint unsigned NOT NULL, `is_locked` tinyint(1) NOT NULL DEFAULT 0,
  `source` enum('auto','manual') NOT NULL DEFAULT 'auto', `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`user_id`), KEY `idx_points_user_tag` (`tag_id`,`is_locked`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='用户积分风控标签';

CREATE TABLE IF NOT EXISTS `system`.`points_risk_logs` (
  `id` bigint unsigned NOT NULL, `user_id` bigint unsigned NOT NULL, `tag_id_before` bigint unsigned DEFAULT NULL, `tag_id_after` bigint unsigned DEFAULT NULL,
  `source` enum('auto','manual') NOT NULL, `operator_id` bigint unsigned DEFAULT NULL, `remark` varchar(255) NOT NULL DEFAULT '',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP, PRIMARY KEY (`id`), KEY `idx_points_risk_log_user` (`user_id`,`created_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='积分风控日志';

INSERT IGNORE INTO `system`.`points_settings`
(`id`,`enabled`,`streak_milestones`) VALUES (1,0,JSON_ARRAY(JSON_OBJECT('days',3,'points',10),JSON_OBJECT('days',7,'points',30),JSON_OBJECT('days',15,'points',60),JSON_OBJECT('days',30,'points',150)));
