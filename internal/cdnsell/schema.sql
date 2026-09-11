CREATE TABLE IF NOT EXISTS `system`.`cdnsell_settings` (
  `id` bigint unsigned NOT NULL, `enabled` tinyint(1) NOT NULL DEFAULT 0,
  `upstream_warn_threshold` decimal(6,2) NOT NULL DEFAULT 20, `customer_warn_thresholds` json NOT NULL,
  `notify_channels` json NOT NULL, `overage_price` bigint NOT NULL DEFAULT 0, `overage_notify_interval` int unsigned NOT NULL DEFAULT 86400,
  `cert_expire_days` int unsigned NOT NULL DEFAULT 30, `reconcile_diff_threshold` decimal(6,2) NOT NULL DEFAULT 1,
  `auto_suspend_enabled` tinyint(1) NOT NULL DEFAULT 0, `auto_restore_enabled` tinyint(1) NOT NULL DEFAULT 0,
  `task_cleanup_days` int unsigned NOT NULL DEFAULT 90, `log_cleanup_days` int unsigned NOT NULL DEFAULT 30,
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP, `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='CanyKsCDN 插件设置';

CREATE TABLE IF NOT EXISTS `system`.`cdnsell_upstream_accounts` (
  `id` bigint unsigned NOT NULL, `name` varchar(100) NOT NULL, `ak_encrypted` text NOT NULL, `sk_encrypted` text NOT NULL,
  `status` tinyint(1) NOT NULL DEFAULT 1, `last_test_time` datetime DEFAULT NULL, `last_test_result` json DEFAULT NULL,
  `api_metrics` json DEFAULT NULL, `sort` int NOT NULL DEFAULT 0, `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP, PRIMARY KEY (`id`), KEY `idx_cdnsell_account_status` (`status`,`sort`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS `system`.`cdnsell_upstream_packages` (
  `id` bigint unsigned NOT NULL, `account_id` bigint unsigned NOT NULL, `name` varchar(120) NOT NULL,
  `capacity_bytes` bigint unsigned NOT NULL, `used_bytes` bigint unsigned NOT NULL DEFAULT 0,
  `effective_time` datetime NOT NULL, `expire_time` datetime NOT NULL, `status` enum('active','expired') NOT NULL DEFAULT 'active',
  `remark` varchar(255) NOT NULL DEFAULT '', `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP, PRIMARY KEY (`id`), KEY `idx_cdnsell_uppkg_account` (`account_id`,`status`,`expire_time`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS `system`.`cdnsell_products` (
  `id` bigint unsigned NOT NULL, `name` varchar(120) NOT NULL, `capacity_bytes` bigint unsigned NOT NULL, `valid_days` int unsigned NOT NULL,
  `price` bigint NOT NULL, `tier_prices` json NOT NULL, `min_qty` int unsigned NOT NULL DEFAULT 1, `max_qty` int unsigned NOT NULL DEFAULT 100,
  `description` text NOT NULL, `is_hot` tinyint(1) NOT NULL DEFAULT 0, `status` tinyint(1) NOT NULL DEFAULT 1, `sort` int NOT NULL DEFAULT 0,
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP, `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`), KEY `idx_cdnsell_product_public` (`status`,`sort`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS `system`.`cdnsell_batches` (
  `id` bigint unsigned NOT NULL, `user_id` bigint unsigned NOT NULL, `product_id` bigint unsigned NOT NULL, `order_id` bigint unsigned NOT NULL,
  `original_bytes` bigint unsigned NOT NULL, `consumed_bytes` bigint unsigned NOT NULL DEFAULT 0, `remaining_bytes` bigint unsigned NOT NULL,
  `effective_time` datetime NOT NULL, `expire_time` datetime NOT NULL, `status` enum('running','exhausted','expired') NOT NULL DEFAULT 'running',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP, PRIMARY KEY (`id`), UNIQUE KEY `uk_cdnsell_batch_order` (`order_id`),
  KEY `idx_cdnsell_batch_fifo` (`user_id`,`status`,`expire_time`,`effective_time`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS `system`.`cdnsell_domains` (
  `id` bigint unsigned NOT NULL, `user_id` bigint unsigned NOT NULL, `account_id` bigint unsigned NOT NULL DEFAULT 0, `domain` varchar(255) NOT NULL,
  `business_type` enum('web','video','download') NOT NULL, `accelerate_region` varchar(40) NOT NULL DEFAULT 'mainland',
  `origin_type` enum('domain','ip') NOT NULL, `origin_host` varchar(255) NOT NULL, `origin_protocol` enum('http','follow','https') NOT NULL,
  `ownership_status` enum('unverified','verified') NOT NULL DEFAULT 'unverified', `ownership_method` enum('dns','file') NOT NULL DEFAULT 'dns',
  `upstream_status` enum('not_created','creating','active','failed','removing') NOT NULL DEFAULT 'not_created', `cname` varchar(255) NOT NULL DEFAULT '',
  `service_no` varchar(100) NOT NULL DEFAULT '', `node_status` varchar(40) NOT NULL DEFAULT '', `sync_version` bigint unsigned NOT NULL DEFAULT 0,
  `sync_time` datetime DEFAULT NULL, `enabled` tinyint(1) NOT NULL DEFAULT 1, `deleted` tinyint(1) NOT NULL DEFAULT 0,
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP, `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`), UNIQUE KEY `uk_cdnsell_domain` (`domain`), KEY `idx_cdnsell_domain_user` (`user_id`,`enabled`,`deleted`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS `system`.`cdnsell_domain_configs` (
  `id` bigint unsigned NOT NULL, `domain_id` bigint unsigned NOT NULL, `config_module` varchar(40) NOT NULL, `local_version` bigint unsigned NOT NULL DEFAULT 0,
  `cloud_version` bigint unsigned NOT NULL DEFAULT 0, `local_config` json NOT NULL, `cloud_config` json NOT NULL, `diff_fields` json NOT NULL,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP, PRIMARY KEY (`id`), UNIQUE KEY `uk_cdnsell_domain_config` (`domain_id`,`config_module`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS `system`.`cdnsell_config_tasks` (
  `id` bigint unsigned NOT NULL, `task_no` varchar(40) NOT NULL, `domain_id` bigint unsigned NOT NULL, `config_module` varchar(40) NOT NULL,
  `version` bigint unsigned NOT NULL, `payload` longtext NOT NULL, `status` enum('pending','running','success','failed','terminated') NOT NULL DEFAULT 'pending',
  `error` text, `attempts` int unsigned NOT NULL DEFAULT 0, `max_attempts` int unsigned NOT NULL DEFAULT 5, `request_id` varchar(100) NOT NULL DEFAULT '',
  `operator_type` enum('member','admin') NOT NULL, `operator_id` bigint unsigned NOT NULL, `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `finished_at` datetime DEFAULT NULL, PRIMARY KEY (`id`), UNIQUE KEY `uk_cdnsell_config_task_no` (`task_no`), KEY `idx_cdnsell_config_task_run` (`status`,`created_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS `system`.`cdnsell_ssl_certs` (
  `id` bigint unsigned NOT NULL, `user_id` bigint unsigned NOT NULL, `domain_id` bigint unsigned NOT NULL, `name` varchar(120) NOT NULL,
  `fingerprint` varchar(128) NOT NULL, `cert_encrypted` longtext NOT NULL, `key_encrypted` longtext NOT NULL, `not_before` datetime NOT NULL, `not_after` datetime NOT NULL,
  `deploy_status` enum('undepolyed','pending','deployed','failed') NOT NULL DEFAULT 'undepolyed', `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`), KEY `idx_cdnsell_cert_expire` (`not_after`,`deploy_status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS `system`.`cdnsell_meter_records` (
  `id` bigint unsigned NOT NULL, `user_id` bigint unsigned NOT NULL, `domain_id` bigint unsigned NOT NULL, `window_start` datetime NOT NULL,
  `window_end` datetime NOT NULL, `confirmed_bytes` bigint unsigned NOT NULL, `revised_bytes` bigint unsigned NOT NULL DEFAULT 0,
  `status` enum('confirmed','revised') NOT NULL DEFAULT 'confirmed', `source` enum('usage_sync','manual') NOT NULL DEFAULT 'usage_sync',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP, `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`), KEY `idx_cdnsell_meter_window` (`user_id`,`domain_id`,`window_start`,`status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS `system`.`cdnsell_overage_bills` (
  `id` bigint unsigned NOT NULL, `bill_no` varchar(40) NOT NULL, `user_id` bigint unsigned NOT NULL, `domain_id` bigint unsigned NOT NULL,
  `start_time` datetime NOT NULL, `end_time` datetime NOT NULL, `traffic_bytes` bigint unsigned NOT NULL, `unit_price` bigint NOT NULL, `amount` bigint NOT NULL,
  `status` enum('unpaid','paid','cancelled','recalculated') NOT NULL DEFAULT 'unpaid', `recalc_history` json NOT NULL,
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP, `paid_at` datetime DEFAULT NULL, PRIMARY KEY (`id`), UNIQUE KEY `uk_cdnsell_bill_no` (`bill_no`),
  KEY `idx_cdnsell_bill_user` (`user_id`,`status`,`created_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS `system`.`cdnsell_orders` (
  `id` bigint unsigned NOT NULL, `order_no` varchar(40) NOT NULL, `user_id` bigint unsigned NOT NULL,
  `order_type` enum('traffic_pack','overage') NOT NULL, `ref_order_id` bigint unsigned NOT NULL, `product_id` bigint unsigned DEFAULT NULL,
  `batch_id` bigint unsigned DEFAULT NULL, `bill_id` bigint unsigned DEFAULT NULL, `traffic_bytes` bigint unsigned NOT NULL, `amount` bigint NOT NULL,
  `snapshot` json NOT NULL, `status` enum('pending','paid','cancelled') NOT NULL DEFAULT 'pending', `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `paid_at` datetime DEFAULT NULL, PRIMARY KEY (`id`), UNIQUE KEY `uk_cdnsell_order_no` (`order_no`), UNIQUE KEY `uk_cdnsell_ref_order` (`ref_order_id`),
  KEY `idx_cdnsell_order_user` (`user_id`,`status`,`created_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS `system`.`cdnsell_tasks` (
  `id` bigint unsigned NOT NULL, `task_no` varchar(40) NOT NULL, `task_type` varchar(40) NOT NULL, `biz_type` varchar(40) NOT NULL DEFAULT '',
  `biz_id` bigint unsigned NOT NULL DEFAULT 0, `status` enum('pending','running','success','failed','terminated','need_manual') NOT NULL DEFAULT 'pending',
  `attempts` int unsigned NOT NULL DEFAULT 0, `max_attempts` int unsigned NOT NULL DEFAULT 5, `next_run_at` datetime DEFAULT NULL, `error` text,
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP, `finished_at` datetime DEFAULT NULL, PRIMARY KEY (`id`), UNIQUE KEY `uk_cdnsell_task_no` (`task_no`),
  KEY `idx_cdnsell_task_run` (`status`,`next_run_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS `system`.`cdnsell_notify_records` (
  `id` bigint unsigned NOT NULL, `user_id` bigint unsigned NOT NULL, `event` varchar(50) NOT NULL, `channel` enum('sms','email','site') NOT NULL,
  `template` varchar(100) NOT NULL DEFAULT '', `content` text NOT NULL, `status` enum('sent','failed') NOT NULL, `error` text,
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP, PRIMARY KEY (`id`), KEY `idx_cdnsell_notify_user` (`user_id`,`event`,`created_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS `system`.`cdnsell_api_logs` (
  `id` bigint unsigned NOT NULL, `account_id` bigint unsigned NOT NULL, `api_name` varchar(80) NOT NULL, `request` json NOT NULL, `response` json NOT NULL,
  `status_code` int NOT NULL DEFAULT 0, `cost_ms` int unsigned NOT NULL DEFAULT 0, `success` tinyint(1) NOT NULL, `error` text,
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP, PRIMARY KEY (`id`), KEY `idx_cdnsell_api_log` (`account_id`,`api_name`,`created_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS `system`.`cdnsell_reconcile_records` (
  `id` bigint unsigned NOT NULL, `reconcile_date` date NOT NULL, `local_total_bytes` bigint unsigned NOT NULL, `cloud_total_bytes` bigint unsigned NOT NULL,
  `diff_bytes` bigint NOT NULL, `diff_ratio` decimal(10,4) NOT NULL, `status` enum('ok','alarm') NOT NULL, `alarm_detail` text,
  `handle_status` enum('unhandled','handled') NOT NULL DEFAULT 'unhandled', `handle_remark` varchar(255) NOT NULL DEFAULT '', `handle_by` bigint unsigned DEFAULT NULL,
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP, PRIMARY KEY (`id`), UNIQUE KEY `uk_cdnsell_reconcile_date` (`reconcile_date`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS `system`.`cdnsell_refresh_tasks` (
  `id` bigint unsigned NOT NULL, `task_no` varchar(40) NOT NULL, `user_id` bigint unsigned NOT NULL, `domain_id` bigint unsigned NOT NULL,
  `type` enum('url_refresh','dir_refresh','url_purge') NOT NULL, `urls` json NOT NULL, `status` enum('pending','running','success','failed') NOT NULL DEFAULT 'pending',
  `progress` json NOT NULL, `error` text, `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP, `finished_at` datetime DEFAULT NULL,
  PRIMARY KEY (`id`), UNIQUE KEY `uk_cdnsell_refresh_task_no` (`task_no`), KEY `idx_cdnsell_refresh_user` (`user_id`,`created_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS `system`.`cdnsell_api_capabilities` (
  `id` bigint unsigned NOT NULL, `api_name` varchar(100) NOT NULL, `capability` varchar(100) NOT NULL, `available` tinyint DEFAULT NULL,
  `note` varchar(255) NOT NULL DEFAULT '', `checked_at` datetime DEFAULT NULL, PRIMARY KEY (`id`), UNIQUE KEY `uk_cdnsell_capability_api` (`api_name`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

INSERT IGNORE INTO `system`.`cdnsell_settings` (`id`,`enabled`,`customer_warn_thresholds`,`notify_channels`)
VALUES (1,0,JSON_ARRAY(JSON_OBJECT('value',30),JSON_OBJECT('value',10)),JSON_OBJECT('site',true,'email',false,'sms',false));
