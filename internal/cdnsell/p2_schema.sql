CREATE TABLE IF NOT EXISTS `system`.`cdnsell_ownership_challenges` (
  `id` bigint unsigned NOT NULL,
  `domain_id` bigint unsigned NOT NULL,
  `method` enum('dns','file') NOT NULL,
  `dns_name` varchar(255) NOT NULL DEFAULT '',
  `file_url` varchar(512) NOT NULL DEFAULT '',
  `token_encrypted` text NOT NULL,
  `status` enum('unverified','verified') NOT NULL DEFAULT 'unverified',
  `last_error` varchar(255) NOT NULL DEFAULT '',
  `verified_at` datetime DEFAULT NULL,
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_cdnsell_ownership_domain` (`domain_id`),
  KEY `idx_cdnsell_ownership_status` (`status`,`updated_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS `system`.`cdnsell_p2_runtime` (
  `id` tinyint unsigned NOT NULL,
  `cloud_writes_enabled` tinyint(1) NOT NULL DEFAULT 0,
  `enabled_by` bigint unsigned DEFAULT NULL,
  `enabled_at` datetime DEFAULT NULL,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

INSERT IGNORE INTO `system`.`cdnsell_p2_runtime` (`id`,`cloud_writes_enabled`) VALUES (1,0);

INSERT IGNORE INTO `system`.`cdnsell_api_capabilities`
  (`id`,`api_name`,`capability`,`available`,`note`)
VALUES
  (920260911001,'RemoveDomain','删除域名',NULL,'P2 待授权实测'),
  (920260911002,'SetDomainEnabled','启停域名',NULL,'P2 待授权实测'),
  (920260911003,'GetDomain','域名状态同步',1,'官方 CDN V3 SDK 支持，待有域名后实测'),
  (920260911004,'GetDomainConfig','配置回读',1,'官方 CDN V3 SDK 支持，待有域名后实测'),
  (920260911005,'GetRefreshProgress','刷新进度',1,'官方 CDN V3 SDK 支持，待写操作授权后实测');
