CREATE TABLE IF NOT EXISTS `system`.`marketplace_settings` (
  `id` bigint unsigned NOT NULL,
  `enabled` tinyint(1) NOT NULL DEFAULT 0,
  `maintenance` tinyint(1) NOT NULL DEFAULT 0,
  `audit_enabled` tinyint(1) NOT NULL DEFAULT 1,
  `require_backup_confirm` tinyint(1) NOT NULL DEFAULT 1,
  `require_realname_listing` tinyint(1) NOT NULL DEFAULT 0,
  `require_realname_purchase` tinyint(1) NOT NULL DEFAULT 0,
  `refund_enabled` tinyint(1) NOT NULL DEFAULT 1,
  `force_settle_after_window` tinyint(1) NOT NULL DEFAULT 1,
  `withdrawal_audit_enabled` tinyint(1) NOT NULL DEFAULT 1,
  `announcement` text NOT NULL,
  `agreement` text NOT NULL,
  `maintenance_tip` varchar(255) NOT NULL DEFAULT '',
  `fee_type` enum('ratio','fixed') NOT NULL DEFAULT 'ratio',
  `fee_bearer` enum('seller','buyer','platform') NOT NULL DEFAULT 'seller',
  `fee_rate` decimal(5,2) NOT NULL DEFAULT 5.00,
  `fee_fixed` decimal(10,2) NOT NULL DEFAULT 0.00,
  `withdraw_fee_type` enum('ratio','fixed','free') NOT NULL DEFAULT 'free',
  `withdraw_fee_rate` decimal(5,2) NOT NULL DEFAULT 0.00,
  `withdraw_fee_fixed` decimal(10,2) NOT NULL DEFAULT 0.00,
  `max_listings_per_user` int unsigned NOT NULL DEFAULT 20,
  `new_user_limit_days` int unsigned NOT NULL DEFAULT 0,
  `listing_valid_minutes` int unsigned NOT NULL DEFAULT 43200,
  `order_lock_minutes` int unsigned NOT NULL DEFAULT 15,
  `settle_freeze_days` int unsigned NOT NULL DEFAULT 3,
  `refund_window_days` int unsigned NOT NULL DEFAULT 3,
  `allow_groups` json NOT NULL,
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='交易市场配置';

CREATE TABLE IF NOT EXISTS `system`.`marketplace_listings` (
  `id` bigint unsigned NOT NULL,
  `listing_no` varchar(32) NOT NULL,
  `seller_id` bigint unsigned NOT NULL,
  `product_id` bigint unsigned NOT NULL COMMENT '本地实例ID',
  `source_product_id` bigint unsigned NOT NULL DEFAULT 0 COMMENT '商品目录ID',
  `group_id` bigint unsigned NOT NULL DEFAULT 0,
  `product_name` varchar(128) NOT NULL,
  `product_type` varchar(32) NOT NULL,
  `region` varchar(64) NOT NULL DEFAULT '',
  `config_snapshot` json NOT NULL,
  `price` decimal(10,2) NOT NULL,
  `original_renewal_price` decimal(10,2) NOT NULL DEFAULT 0.00,
  `remaining_days` int unsigned NOT NULL DEFAULT 0,
  `status` enum('pending','on_sale','locked','sold','refunded','off_shelf','rejected') NOT NULL DEFAULT 'pending',
  `audit_status` enum('pending','passed','rejected') NOT NULL DEFAULT 'pending',
  `audit_remark` varchar(512) NOT NULL DEFAULT '',
  `auditor_id` bigint unsigned DEFAULT NULL,
  `views` int unsigned NOT NULL DEFAULT 0,
  `favorite_count` int unsigned NOT NULL DEFAULT 0,
  `seller_note` text NOT NULL,
  `is_recommended` tinyint(1) NOT NULL DEFAULT 0,
  `expires_at` datetime NOT NULL,
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`), UNIQUE KEY `uk_marketplace_listing_no` (`listing_no`),
  KEY `idx_marketplace_listing_public` (`status`,`audit_status`,`expires_at`,`is_recommended`),
  KEY `idx_marketplace_listing_seller` (`seller_id`,`status`),
  KEY `idx_marketplace_listing_product` (`product_id`,`status`),
  KEY `idx_marketplace_listing_group` (`group_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='交易市场挂牌';

CREATE TABLE IF NOT EXISTS `system`.`marketplace_listing_prices` (
  `id` bigint unsigned NOT NULL, `listing_id` bigint unsigned NOT NULL,
  `old_price` decimal(10,2) NOT NULL, `new_price` decimal(10,2) NOT NULL,
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`), KEY `idx_marketplace_price_listing` (`listing_id`,`created_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='挂牌调价记录';

CREATE TABLE IF NOT EXISTS `system`.`marketplace_favorites` (
  `id` bigint unsigned NOT NULL, `listing_id` bigint unsigned NOT NULL,
  `user_id` bigint unsigned NOT NULL, `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`), UNIQUE KEY `uk_marketplace_favorite` (`user_id`,`listing_id`),
  KEY `idx_marketplace_favorite_listing` (`listing_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='挂牌收藏';

CREATE TABLE IF NOT EXISTS `system`.`marketplace_orders` (
  `id` bigint unsigned NOT NULL, `order_no` varchar(32) NOT NULL,
  `listing_id` bigint unsigned NOT NULL, `buyer_id` bigint unsigned NOT NULL,
  `seller_id` bigint unsigned NOT NULL, `amount` decimal(10,2) NOT NULL,
  `pay_amount` decimal(10,2) NOT NULL, `fee` decimal(10,2) NOT NULL DEFAULT 0.00,
  `fee_bearer` enum('seller','buyer','platform') NOT NULL,
  `seller_income` decimal(10,2) NOT NULL,
  `status` enum('pending_pay','locked','paid','settled','refunded','cancelled') NOT NULL DEFAULT 'pending_pay',
  `payment_method` varchar(16) NOT NULL DEFAULT '',
  `lock_expires_at` datetime NOT NULL, `paid_at` datetime DEFAULT NULL,
  `settle_at` datetime DEFAULT NULL, `refund_window_end` datetime DEFAULT NULL,
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`), UNIQUE KEY `uk_marketplace_order_no` (`order_no`),
  KEY `idx_marketplace_order_buyer` (`buyer_id`,`status`),
  KEY `idx_marketplace_order_seller` (`seller_id`,`status`),
  KEY `idx_marketplace_order_listing` (`listing_id`,`status`),
  KEY `idx_marketplace_order_maintenance` (`status`,`lock_expires_at`,`settle_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='交易市场订单';

CREATE TABLE IF NOT EXISTS `system`.`marketplace_wants` (
  `id` bigint unsigned NOT NULL, `want_no` varchar(32) NOT NULL, `user_id` bigint unsigned NOT NULL,
  `title` varchar(128) NOT NULL, `budget_min` decimal(10,2) NOT NULL,
  `budget_max` decimal(10,2) NOT NULL, `region` varchar(64) NOT NULL DEFAULT '',
  `description` text NOT NULL, `tags` json NOT NULL,
  `status` enum('open','matched','closed') NOT NULL DEFAULT 'open',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`), UNIQUE KEY `uk_marketplace_want_no` (`want_no`),
  KEY `idx_marketplace_want_public` (`status`,`created_at`), KEY `idx_marketplace_want_user` (`user_id`,`status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='交易市场求购';

CREATE TABLE IF NOT EXISTS `system`.`marketplace_want_offers` (
  `id` bigint unsigned NOT NULL, `want_id` bigint unsigned NOT NULL, `seller_id` bigint unsigned NOT NULL,
  `listing_id` bigint unsigned DEFAULT NULL, `price` decimal(10,2) NOT NULL,
  `message` varchar(1000) NOT NULL DEFAULT '', `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`), KEY `idx_marketplace_offer_want` (`want_id`,`created_at`),
  KEY `idx_marketplace_offer_seller` (`seller_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='求购报价';

CREATE TABLE IF NOT EXISTS `system`.`marketplace_wallet` (
  `user_id` bigint unsigned NOT NULL, `balance` decimal(10,2) NOT NULL DEFAULT 0.00,
  `frozen_balance` decimal(10,2) NOT NULL DEFAULT 0.00,
  `total_income` decimal(10,2) NOT NULL DEFAULT 0.00,
  `total_withdrawn` decimal(10,2) NOT NULL DEFAULT 0.00,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`user_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='交易市场卖家钱包';

CREATE TABLE IF NOT EXISTS `system`.`marketplace_flows` (
  `id` bigint unsigned NOT NULL, `flow_no` varchar(32) NOT NULL, `user_id` bigint unsigned NOT NULL,
  `type` enum('buyer_paid','seller_pending','settled','refund_reversed','withdraw','withdraw_reject') NOT NULL,
  `direction` enum('in','out') NOT NULL, `amount` decimal(10,2) NOT NULL,
  `balance_after` decimal(10,2) NOT NULL, `ref_id` bigint unsigned NOT NULL,
  `remark` varchar(255) NOT NULL DEFAULT '', `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`), UNIQUE KEY `uk_marketplace_flow_no` (`flow_no`),
  KEY `idx_marketplace_flow_user` (`user_id`,`created_at`), KEY `idx_marketplace_flow_ref` (`ref_id`,`type`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='交易市场资金流水';

CREATE TABLE IF NOT EXISTS `system`.`marketplace_withdrawals` (
  `id` bigint unsigned NOT NULL, `withdraw_no` varchar(32) NOT NULL, `user_id` bigint unsigned NOT NULL,
  `amount` decimal(10,2) NOT NULL, `fee` decimal(10,2) NOT NULL DEFAULT 0.00,
  `actual_amount` decimal(10,2) NOT NULL,
  `status` enum('pending','approved','rejected','paid') NOT NULL DEFAULT 'pending',
  `auditor_id` bigint unsigned DEFAULT NULL, `remark` varchar(512) NOT NULL DEFAULT '',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`), UNIQUE KEY `uk_marketplace_withdraw_no` (`withdraw_no`),
  KEY `idx_marketplace_withdraw_user` (`user_id`,`status`), KEY `idx_marketplace_withdraw_admin` (`status`,`created_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='交易市场提现';

CREATE TABLE IF NOT EXISTS `system`.`marketplace_reports` (
  `id` bigint unsigned NOT NULL, `listing_id` bigint unsigned NOT NULL, `reporter_id` bigint unsigned NOT NULL,
  `reason` varchar(64) NOT NULL, `detail` varchar(1000) NOT NULL DEFAULT '',
  `status` enum('pending','processed','ignored') NOT NULL DEFAULT 'pending',
  `handler_id` bigint unsigned DEFAULT NULL, `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`), KEY `idx_marketplace_report_admin` (`status`,`created_at`),
  KEY `idx_marketplace_report_listing` (`listing_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='交易市场举报';

