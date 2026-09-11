CREATE TABLE IF NOT EXISTS `system`.`groupbuy_activities` (
  `id` bigint unsigned NOT NULL,
  `product_id` bigint unsigned NOT NULL,
  `product_name` varchar(128) NOT NULL,
  `group_id` bigint unsigned NOT NULL DEFAULT 0,
  `group_name` varchar(128) NOT NULL DEFAULT '',
  `billing_cycle` varchar(32) NOT NULL,
  `billing_cycle_name` varchar(32) NOT NULL,
  `config_options` json NOT NULL,
  `config_description` varchar(512) NOT NULL DEFAULT '',
  `original_price` bigint NOT NULL,
  `price_mode` tinyint unsigned NOT NULL DEFAULT 1,
  `two_price` bigint DEFAULT NULL, `two_discount` int DEFAULT NULL,
  `three_price` bigint DEFAULT NULL, `three_discount` int DEFAULT NULL,
  `five_price` bigint DEFAULT NULL, `five_discount` int DEFAULT NULL,
  `two_leader_price` bigint DEFAULT NULL, `two_leader_discount` int DEFAULT NULL,
  `three_leader_price` bigint DEFAULT NULL, `three_leader_discount` int DEFAULT NULL,
  `five_leader_price` bigint DEFAULT NULL, `five_leader_discount` int DEFAULT NULL,
  `renew_type` tinyint unsigned NOT NULL DEFAULT 1,
  `timeout_days` int unsigned NOT NULL DEFAULT 3,
  `end_time` datetime NOT NULL,
  `status` tinyint unsigned NOT NULL DEFAULT 0,
  `sort` int NOT NULL DEFAULT 0,
  `is_public_group_enabled` tinyint(1) NOT NULL DEFAULT 1,
  `force_public` tinyint(1) NOT NULL DEFAULT 0,
  `slogan` varchar(255) NOT NULL DEFAULT '',
  `require_certification` tinyint(1) NOT NULL DEFAULT 0,
  `purchase_limit` int unsigned NOT NULL DEFAULT 0,
  `member_restrict_mode` tinyint unsigned NOT NULL DEFAULT 0,
  `hide_for_old_user` tinyint(1) NOT NULL DEFAULT 0,
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`), KEY `idx_groupbuy_activity_product` (`product_id`),
  KEY `idx_groupbuy_activity_state` (`status`,`end_time`,`sort`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='拼团活动';

CREATE TABLE IF NOT EXISTS `system`.`groupbuy_records` (
  `id` bigint unsigned NOT NULL,
  `activity_id` bigint unsigned NOT NULL,
  `client_id` bigint unsigned NOT NULL,
  `group_type` varchar(8) NOT NULL,
  `is_leader` tinyint(1) NOT NULL DEFAULT 0,
  `leader_id` bigint unsigned NOT NULL,
  `current_num` int unsigned NOT NULL DEFAULT 1,
  `need_num` int unsigned NOT NULL,
  `pay_amount` bigint NOT NULL,
  `group_price` bigint NOT NULL,
  `level_discount_amount` bigint NOT NULL DEFAULT 0,
  `order_id` bigint unsigned DEFAULT NULL,
  `host_id` bigint unsigned DEFAULT NULL,
  `invite_code` varchar(24) NOT NULL,
  `is_public` tinyint(1) NOT NULL DEFAULT 1,
  `force_close_public` tinyint(1) NOT NULL DEFAULT 0,
  `status` tinyint unsigned NOT NULL DEFAULT 1,
  `timeout_time` datetime NOT NULL,
  `success_time` datetime DEFAULT NULL,
  `is_notice` tinyint(1) NOT NULL DEFAULT 0,
  `recommend_identify` varchar(64) NOT NULL DEFAULT '',
  `recommend_client_id` bigint unsigned DEFAULT NULL,
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`), UNIQUE KEY `uk_groupbuy_invite` (`invite_code`),
  KEY `idx_groupbuy_activity` (`activity_id`), KEY `idx_groupbuy_client` (`client_id`),
  KEY `idx_groupbuy_leader` (`leader_id`), KEY `idx_groupbuy_status` (`status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='拼团参与记录';

CREATE TABLE IF NOT EXISTS `system`.`groupbuy_settings` (
  `id` bigint unsigned NOT NULL,
  `client_level_discount_enable` tinyint(1) NOT NULL DEFAULT 0,
  `timeout_remind_hours` int unsigned NOT NULL DEFAULT 24,
  `show_member_name` tinyint(1) NOT NULL DEFAULT 1,
  `show_member_phone` tinyint(1) NOT NULL DEFAULT 0,
  `new_user_group_limit` int unsigned NOT NULL DEFAULT 1,
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='拼团设置';

INSERT IGNORE INTO `system`.`system_configs` (`id`,`key`,`value`,`description`)
VALUES (UUID_SHORT(), 'groupbuy.enabled', '0', '拼团模块总开关');
INSERT INTO `system`.`groupbuy_settings` (`id`)
SELECT UUID_SHORT() WHERE NOT EXISTS (SELECT 1 FROM `system`.`groupbuy_settings`);
