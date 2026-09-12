-- INSTALLER SOURCE NOTE:
-- scripts/install.sh never executes this historical baseline verbatim. It
-- removes every DROP TABLE statement and rewrites CREATE TABLE as
-- CREATE TABLE IF NOT EXISTS before piping the result to MySQL. Keep this file
-- aligned with the project's authoritative 11-schema baseline.
-- ============================================
-- DDYIDC 系统数据库建表脚本
-- 生成时间: 2026-09-05
-- 数据库: MySQL 8.0+ / InnoDB / utf8mb4_unicode_ci
-- 表数量: 111 张 / 11 个 Schema
-- 主键: 雪花ID(bigint unsigned) / 金额: 分(bigint) / 软删除: deleted_at
-- ============================================

SET NAMES utf8mb4;
SET FOREIGN_KEY_CHECKS = 0;

-- ============================================
-- 创建 Schema（按业务域分库）
-- ============================================
CREATE SCHEMA IF NOT EXISTS `agency` DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
CREATE SCHEMA IF NOT EXISTS `billing` DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
CREATE SCHEMA IF NOT EXISTS `common` DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
CREATE SCHEMA IF NOT EXISTS `customer` DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
CREATE SCHEMA IF NOT EXISTS `instance` DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
CREATE SCHEMA IF NOT EXISTS `order` DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
CREATE SCHEMA IF NOT EXISTS `payment` DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
CREATE SCHEMA IF NOT EXISTS `product` DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
CREATE SCHEMA IF NOT EXISTS `system` DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
CREATE SCHEMA IF NOT EXISTS `ticket` DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
-- 营销模块的扩展表位于同一脚本；确保迁移时不会因缺少 Schema 失败。
CREATE SCHEMA IF NOT EXISTS `marketing` DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

-- ============================================
-- 域: 代理域 (agency) - 9 张表
-- ============================================

DROP TABLE IF EXISTS `agency`.`agencies`;
CREATE TABLE `agency`.`agencies` (
  `id` bigint unsigned NOT NULL,
  `customer_id` bigint unsigned NOT NULL COMMENT '关联客户 ID（唯一）',
  `parent_id` bigint unsigned DEFAULT 0 COMMENT '上级经销商（0=顶级）',
  `name` varchar(64) NOT NULL COMMENT '经销商名称',
  `level_id` bigint unsigned DEFAULT 1 COMMENT '关联 agency_levels.id（等级配置，可升降级）',
  `promotion_code` varchar(16) NOT NULL COMMENT '唯一推广码（如 DDY-A8K2M）',
  `monthly_sales` bigint DEFAULT 0 COMMENT '本月销售额（分，每月1日清零累计）',
  `total_sales` bigint DEFAULT 0 COMMENT '累计销售额（分）',
  `total_commission` bigint DEFAULT 0 COMMENT '累计佣金（分）',
  `balance` bigint DEFAULT 0 COMMENT '可提现佣金余额（分）',
  `frozen_amount` bigint DEFAULT 0 COMMENT '冻结中佣金（分，提现审核中）',
  `credit_limit` bigint DEFAULT 0 COMMENT '授信额度（分）',
  `status` tinyint unsigned DEFAULT 1 COMMENT '1正常 2冻结 3退出',
  `level_updated_at` datetime DEFAULT NULL COMMENT '最近一次等级变更时间',
  `brand_name` varchar(64) DEFAULT NULL COMMENT '白标品牌名',
  `brand_logo` varchar(255) DEFAULT NULL COMMENT '白标 Logo',
  `domain` varchar(128) DEFAULT NULL COMMENT '白标域名',
  `settlement_account` json DEFAULT NULL COMMENT '结算账户 {type:alipay/wechat/bank, account, name}',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (id),UNIQUE KEY uk_customer (customer_id),UNIQUE KEY uk_promotion_code (promotion_code),KEY idx_parent (parent_id),KEY idx_level (level_id),KEY idx_status (status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='经销商';

DROP TABLE IF EXISTS `agency`.`agency_customers`;
CREATE TABLE `agency`.`agency_customers` (
  `agency_id` bigint unsigned NOT NULL COMMENT '经销商 ID',
  `customer_id` bigint unsigned NOT NULL COMMENT '子客 ID',
  `relation_type` tinyint unsigned NOT NULL COMMENT '1直接 2间接',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  PRIMARY KEY (agency_id, customer_id),KEY idx_customer (customer_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='经销商-子客关联';

DROP TABLE IF EXISTS `agency`.`agency_vouchers`;
CREATE TABLE `agency`.`agency_vouchers` (
  `id` bigint unsigned NOT NULL,
  `agency_id` bigint unsigned NOT NULL COMMENT '经销商 ID',
  `total_amount` bigint NOT NULL COMMENT '总额度（分）',
  `used_amount` bigint DEFAULT 0 COMMENT '已用（分）',
  `status` tinyint unsigned DEFAULT 1 COMMENT '1正常 2冻结',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (id),KEY idx_agency (agency_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='代金券额度池';

DROP TABLE IF EXISTS `agency`.`commissions`;
CREATE TABLE `agency`.`commissions` (
  `id` bigint unsigned NOT NULL,
  `agency_id` bigint unsigned NOT NULL,
  `customer_id` bigint unsigned NOT NULL COMMENT '子客 ID',
  `order_id` bigint unsigned NOT NULL COMMENT '关联订单',
  `order_amount` bigint NOT NULL COMMENT '订单金额（分）',
  `rate` decimal(5,4) NOT NULL COMMENT '佣金比例',
  `amount` bigint NOT NULL COMMENT '佣金金额（分）',
  `status` tinyint unsigned DEFAULT 1 COMMENT '1待结算 2已结算 3已取消',
  `settled_at` datetime DEFAULT NULL,
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (id),KEY idx_agency (agency_id, status),KEY idx_order (order_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='佣金记录';

DROP TABLE IF EXISTS `agency`.`agency_levels`;
CREATE TABLE `agency`.`agency_levels` (
  `id` bigint unsigned NOT NULL,
  `level` int unsigned NOT NULL COMMENT '等级序号（1最低，N最高）',
  `name` varchar(32) NOT NULL COMMENT '等级名称（标准/银牌/金牌/钻石/旗舰）',
  `monthly_sales_threshold` bigint DEFAULT 0 COMMENT '月销售额门槛（分）',
  `base_commission_rate` decimal(5,4) DEFAULT 0 COMMENT '基础返点比例',
  `new_customer_bonus` decimal(5,4) DEFAULT 0 COMMENT '新客加成比例',
  `renewal_commission_rate` decimal(5,4) DEFAULT 0 COMMENT '续费返点比例',
  `excess_bonus_rate` decimal(5,4) DEFAULT 0 COMMENT '超额奖励比例',
  `agent_discount` decimal(3,2) DEFAULT 1.00 COMMENT '代理折扣（0.75=75折）',
  `white_label_level` tinyint unsigned DEFAULT 0 COMMENT '白标权限 0无 1品牌Logo 2品牌+域名 3完全白标',
  `credit_limit` bigint DEFAULT 0 COMMENT '授信额度（分）',
  `upgrade_months` int unsigned DEFAULT 2 COMMENT '连续N个月达标可升级',
  `downgrade_months` int unsigned DEFAULT 3 COMMENT '连续N个月不达标降级',
  `status` tinyint unsigned DEFAULT 1 COMMENT '1启用 2禁用',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (id),UNIQUE KEY uk_level (level),KEY idx_status (status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='代理等级配置';

DROP TABLE IF EXISTS `agency`.`promotion_links`;
CREATE TABLE `agency`.`promotion_links` (
  `id` bigint unsigned NOT NULL,
  `agency_id` bigint unsigned NOT NULL COMMENT '经销商 ID',
  `link_type` varchar(16) NOT NULL COMMENT 'link（链接）/ qrcode（二维码）/ poster（海报）',
  `target_url` varchar(512) NOT NULL COMMENT '推广目标URL（带推广码参数）',
  `click_count` int unsigned DEFAULT 0 COMMENT '点击次数',
  `register_count` int unsigned DEFAULT 0 COMMENT '注册转化数',
  `order_count` int unsigned DEFAULT 0 COMMENT '下单转化数',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (id),KEY idx_agency (agency_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='推广链接追踪';

DROP TABLE IF EXISTS `agency`.`agency_settlements`;
CREATE TABLE `agency`.`agency_settlements` (
  `id` bigint unsigned NOT NULL,
  `settlement_no` varchar(32) NOT NULL COMMENT '结算单号（唯一）',
  `agency_id` bigint unsigned NOT NULL COMMENT '经销商 ID',
  `period` char(7) NOT NULL COMMENT '结算周期 YYYY-MM',
  `total_commission` bigint DEFAULT 0 COMMENT '本期佣金总额（分）',
  `order_count` int unsigned DEFAULT 0 COMMENT '关联订单数',
  `status` tinyint unsigned DEFAULT 1 COMMENT '1待结算 2已结算 3已驳回',
  `settled_at` datetime DEFAULT NULL COMMENT '结算时间',
  `remark` varchar(255) DEFAULT NULL COMMENT '备注',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (id),UNIQUE KEY uk_settlement_no (settlement_no),KEY idx_agency_period (agency_id, period),KEY idx_status (status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='佣金结算单';

DROP TABLE IF EXISTS `agency`.`agency_withdrawals`;
CREATE TABLE `agency`.`agency_withdrawals` (
  `id` bigint unsigned NOT NULL,
  `withdrawal_no` varchar(32) NOT NULL COMMENT '提现单号（唯一）',
  `agency_id` bigint unsigned NOT NULL COMMENT '经销商 ID',
  `amount` bigint NOT NULL COMMENT '提现金额（分）',
  `fee` bigint DEFAULT 0 COMMENT '手续费（分）',
  `actual_amount` bigint NOT NULL COMMENT '实际到账（分）',
  `account_type` varchar(16) NOT NULL COMMENT 'alipay/wechat/bank',
  `account_info` json NOT NULL COMMENT '收款账户信息',
  `status` tinyint unsigned DEFAULT 1 COMMENT '1待审核 2审核通过 3已打款 4已驳回',
  `auditor_id` bigint unsigned DEFAULT NULL COMMENT '审核员 ID',
  `audit_remark` varchar(255) DEFAULT NULL COMMENT '审核备注',
  `paid_at` datetime DEFAULT NULL COMMENT '打款时间',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (id),UNIQUE KEY uk_withdrawal_no (withdrawal_no),KEY idx_agency (agency_id, status),KEY idx_status (status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='提现申请';

DROP TABLE IF EXISTS `agency`.`commission_rules`;
CREATE TABLE `agency`.`commission_rules` (
  `id` bigint unsigned NOT NULL,
  `product_id` bigint unsigned DEFAULT NULL COMMENT '关联商品（NULL=全局默认）',
  `level_id` bigint unsigned DEFAULT NULL COMMENT '关联等级（NULL=所有等级）',
  `order_type` tinyint unsigned DEFAULT 0 COMMENT '0全部 1新客 2续费 3升级',
  `commission_rate` decimal(5,4) NOT NULL COMMENT '佣金比例（覆盖等级默认值）',
  `status` tinyint unsigned DEFAULT 1 COMMENT '1启用 2禁用',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (id),KEY idx_product_level (product_id, level_id),KEY idx_status (status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='佣金规则配置';

-- ============================================
-- 域: 计费域 (billing) - 7 张表
-- ============================================

DROP TABLE IF EXISTS `billing`.`accounts`;
CREATE TABLE `billing`.`accounts` (
  `id` bigint unsigned NOT NULL,
  `customer_id` bigint unsigned NOT NULL COMMENT '客户 ID（唯一）',
  `balance` bigint DEFAULT 0 COMMENT '可用余额（分）',
  `frozen_amount` bigint DEFAULT 0 COMMENT '冻结金额（分）',
  `credit_limit` bigint DEFAULT 0 COMMENT '信用额度（分）',
  `total_recharge` bigint DEFAULT 0 COMMENT '累计充值（分）',
  `total_consume` bigint DEFAULT 0 COMMENT '累计消费（分）',
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  PRIMARY KEY (id),UNIQUE KEY uk_customer (customer_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='账户余额';

DROP TABLE IF EXISTS `billing`.`transactions`;
CREATE TABLE `billing`.`transactions` (
  `id` bigint unsigned NOT NULL,
  `tx_no` varchar(32) NOT NULL COMMENT '流水号（唯一）',
  `customer_id` bigint unsigned NOT NULL COMMENT '客户 ID',
  `type` tinyint unsigned NOT NULL COMMENT '1充值 2消费 3退款 4提现 5佣金 6调整',
  `direction` tinyint unsigned NOT NULL COMMENT '1收入 2支出',
  `amount` bigint NOT NULL COMMENT '变动金额（分，正数）',
  `balance_before` bigint NOT NULL COMMENT '变动前余额',
  `balance_after` bigint NOT NULL COMMENT '变动后余额',
  `related_type` varchar(32) DEFAULT NULL COMMENT '关联类型（order/payment/refund/recharge）',
  `related_id` bigint unsigned DEFAULT NULL COMMENT '关联 ID',
  `remark` varchar(255) DEFAULT NULL COMMENT '备注',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (id),UNIQUE KEY uk_tx_no (tx_no),KEY idx_customer (customer_id, created_at),KEY idx_related (related_type, related_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='交易流水/账本';

DROP TABLE IF EXISTS `billing`.`bills`;
CREATE TABLE `billing`.`bills` (
  `id` bigint unsigned NOT NULL,
  `bill_no` varchar(32) NOT NULL COMMENT '账单号（唯一）',
  `customer_id` bigint unsigned NOT NULL,
  `period` char(6) NOT NULL COMMENT '账期 YYYYMM',
  `total_amount` bigint DEFAULT 0 COMMENT '账单总额（分）',
  `paid_amount` bigint DEFAULT 0 COMMENT '已付金额（分）',
  `status` tinyint unsigned DEFAULT 1 COMMENT '1未出账 2已出账 3已支付 4部分支付 5已逾期',
  `generated_at` datetime DEFAULT NULL COMMENT '出账时间',
  `due_at` datetime DEFAULT NULL COMMENT '到期时间',
  `paid_at` datetime DEFAULT NULL COMMENT '结清时间',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (id),UNIQUE KEY uk_bill_no (bill_no),UNIQUE KEY uk_customer_period (customer_id, period),KEY idx_status (status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='账单';

DROP TABLE IF EXISTS `billing`.`bill_items`;
CREATE TABLE `billing`.`bill_items` (
  `id` bigint unsigned NOT NULL,
  `bill_id` bigint unsigned NOT NULL COMMENT '账单 ID',
  `instance_id` bigint unsigned DEFAULT NULL COMMENT '实例 ID',
  `product_name` varchar(128) NOT NULL COMMENT '产品名称（快照）',
  `duration` varchar(32) DEFAULT NULL COMMENT '计费周期描述',
  `amount` bigint NOT NULL COMMENT '金额（分）',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (id),KEY idx_bill (bill_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='账单明细';

DROP TABLE IF EXISTS `billing`.`invoices`;
CREATE TABLE `billing`.`invoices` (
  `id` bigint unsigned NOT NULL,
  `invoice_no` varchar(32) DEFAULT NULL COMMENT '发票号码',
  `customer_id` bigint unsigned NOT NULL,
  `amount` bigint NOT NULL COMMENT '开票金额（分）',
  `type` tinyint unsigned DEFAULT 1 COMMENT '1普通发票 2增值税专用发票 3电子发票',
  `title` varchar(128) NOT NULL COMMENT '发票抬头',
  `tax_no` varchar(32) DEFAULT NULL COMMENT '税号',
  `content` varchar(64) NOT NULL COMMENT '开票内容',
  `address` varchar(255) DEFAULT NULL COMMENT '邮寄地址',
  `status` tinyint unsigned DEFAULT 1 COMMENT '1申请中 2已开票 3已邮寄 4已作废',
  `mailed_at` datetime DEFAULT NULL,
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (id),KEY idx_customer (customer_id),KEY idx_status (status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='发票';

DROP TABLE IF EXISTS `billing`.`coupons`;
CREATE TABLE `billing`.`coupons` (
  `id` bigint unsigned NOT NULL,
  `code` varchar(32) NOT NULL COMMENT '券码（唯一）',
  `customer_id` bigint unsigned DEFAULT NULL COMMENT '归属客户（NULL=通用券）',
  `type` tinyint unsigned DEFAULT 1 COMMENT '1满减 2折扣 3代金',
  `amount` bigint DEFAULT 0 COMMENT '金额（分，满减/代金）',
  `percent` decimal(5,2) DEFAULT NULL COMMENT '折扣（0.00-1.00）',
  `min_amount` bigint DEFAULT 0 COMMENT '最低消费（分）',
  `scope` json DEFAULT NULL COMMENT '适用范围 {product_ids:[], group_ids:[]}',
  `status` tinyint unsigned DEFAULT 1 COMMENT '1未使用 2已使用 3已过期 4已作废',
  `used_order_id` bigint unsigned DEFAULT NULL COMMENT '使用的订单',
  `used_at` datetime DEFAULT NULL,
  `expires_at` datetime DEFAULT NULL COMMENT '过期时间',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (id),UNIQUE KEY uk_code (code),KEY idx_customer (customer_id, status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='优惠券/代金券';

DROP TABLE IF EXISTS `billing`.`discounts`;
CREATE TABLE `billing`.`discounts` (
  `id` bigint unsigned NOT NULL,
  `name` varchar(64) NOT NULL COMMENT '规则名称',
  `type` tinyint unsigned DEFAULT 1 COMMENT '1比例折扣 2固定金额',
  `value` bigint NOT NULL COMMENT '折扣值（分 或 千分比）',
  `scope` json DEFAULT NULL COMMENT '适用范围',
  `target_level` tinyint unsigned DEFAULT NULL COMMENT '适用客户等级',
  `status` tinyint unsigned DEFAULT 1 COMMENT '1启用 2禁用',
  `start_at` datetime DEFAULT NULL,
  `end_at` datetime DEFAULT NULL,
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (id),KEY idx_status (status, start_at, end_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='折扣规则';

-- ============================================
-- 域: 通知与通用域 (common) - 5 张表
-- ============================================

DROP TABLE IF EXISTS `common`.`notifications`;
CREATE TABLE `common`.`notifications` (
  `id` bigint unsigned NOT NULL,
  `customer_id` bigint unsigned DEFAULT NULL COMMENT '接收客户（NULL=广播）',
  `channel` varchar(16) DEFAULT 'sms' COMMENT '通知渠道（sms/email/inapp/webhook）',
  `template_code` varchar(64) NOT NULL COMMENT '模板编码',
  `params` json DEFAULT NULL COMMENT '模板参数',
  `subject` varchar(255) DEFAULT NULL COMMENT '标题（邮件）',
  `content` text DEFAULT NULL COMMENT '渲染后内容',
  `status` tinyint unsigned DEFAULT 1 COMMENT '1待发送 2成功 3失败',
  `fail_reason` varchar(255) DEFAULT NULL,
  `retry_count` int unsigned DEFAULT 0,
  `sent_at` datetime DEFAULT NULL,
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (id),KEY idx_customer (customer_id),KEY idx_status_channel (status, channel),KEY idx_created_at (created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='通知记录';

DROP TABLE IF EXISTS `common`.`notification_templates`;
CREATE TABLE `common`.`notification_templates` (
  `id` bigint unsigned NOT NULL,
  `code` varchar(64) NOT NULL COMMENT '模板编码（唯一）',
  `name` varchar(64) NOT NULL COMMENT '模板名称',
  `channel` varchar(16) NOT NULL COMMENT 'sms/email/inapp',
  `title` varchar(128) DEFAULT NULL COMMENT '标题（邮件）',
  `content` text NOT NULL COMMENT '模板内容（含 {{变量}}）',
  `variables` json DEFAULT NULL COMMENT '变量列表 [{name,desc}]',
  `status` tinyint unsigned DEFAULT 1 COMMENT '1启用 2禁用',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (id),UNIQUE KEY uk_code_channel (code, channel)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='通知模板';

DROP TABLE IF EXISTS `common`.`files`;
CREATE TABLE `common`.`files` (
  `id` bigint unsigned NOT NULL,
  `name` varchar(255) NOT NULL COMMENT '原始文件名',
  `path` varchar(512) NOT NULL COMMENT '存储路径',
  `url` varchar(512) DEFAULT NULL COMMENT '访问 URL',
  `size` bigint unsigned NOT NULL COMMENT '文件大小（字节）',
  `mime` varchar(64) DEFAULT NULL COMMENT 'MIME 类型',
  `storage` varchar(16) DEFAULT 'local' COMMENT 'local/minio/oss',
  `uploader_type` tinyint unsigned DEFAULT 1 COMMENT '上传者类型（1客户 2管理员）',
  `uploader_id` bigint unsigned NOT NULL,
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (id),KEY idx_uploader (uploader_type, uploader_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='文件记录';

DROP TABLE IF EXISTS `common`.`scheduled_tasks`;
CREATE TABLE `common`.`scheduled_tasks` (
  `id` bigint unsigned NOT NULL,
  `name` varchar(64) NOT NULL COMMENT '任务名（唯一）',
  `cron` varchar(64) NOT NULL COMMENT 'cron 表达式',
  `type` varchar(32) NOT NULL COMMENT '任务类型',
  `status` tinyint unsigned DEFAULT 1 COMMENT '1启用 2禁用',
  `last_run_at` datetime DEFAULT NULL COMMENT '上次运行',
  `last_run_status` tinyint unsigned DEFAULT NULL COMMENT '1成功 2失败',
  `last_run_duration` int unsigned DEFAULT NULL COMMENT '耗时（毫秒）',
  `next_run_at` datetime DEFAULT NULL COMMENT '下次运行',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (id),UNIQUE KEY uk_name (name),KEY idx_next_run (next_run_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='定时任务记录';

DROP TABLE IF EXISTS `common`.`login_channels`;
CREATE TABLE `common`.`login_channels` (
  `id` bigint unsigned NOT NULL,
  `channel_code` varchar(32) NOT NULL COMMENT '渠道标识（wechat/qq/alipay/github），唯一',
  `channel_name` varchar(64) NOT NULL COMMENT '渠道名称（微信登录/QQ登录）',
  `icon` varchar(255) DEFAULT NULL COMMENT '图标（URL 或 emoji）',
  `sort` int unsigned DEFAULT 0 COMMENT '排序（小在前）',
  `status` tinyint unsigned DEFAULT 2 COMMENT '1启用 2禁用（默认禁用，配置后手动启用）',
  `config` json DEFAULT NULL COMMENT '渠道配置（app_id/app_secret/callback_url 等）',
  `is_builtin` tinyint unsigned DEFAULT 1 COMMENT '1内置 0插件扩展',
  `plugin_name` varchar(64) DEFAULT NULL COMMENT '插件名称（插件渠道时）',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (id),
  UNIQUE KEY uk_channel_code (channel_code),
  KEY idx_status (status),
  KEY idx_sort (sort)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='登录渠道配置';

-- ============================================
-- 域: 客户域 (customer) - 5 张表
-- ============================================

DROP TABLE IF EXISTS `customer`.`customers`;
CREATE TABLE `customer`.`customers` (
  `id` bigint unsigned NOT NULL COMMENT '客户 ID',
  `tenant_id` bigint unsigned DEFAULT 0 COMMENT '租户 ID',
  `username` varchar(64) NOT NULL COMMENT '用户名（唯一，登录用）',
  `email` varchar(128) DEFAULT NULL COMMENT '邮箱（唯一，可登录）',
  `phone` varchar(20) DEFAULT NULL COMMENT '手机号（唯一，可登录）',
  `password_hash` varchar(255) NOT NULL COMMENT 'bcrypt 哈希',
  `real_name` varchar(64) DEFAULT NULL COMMENT '真实姓名（实名认证后）',
  `nickname` varchar(64) DEFAULT NULL COMMENT '昵称',
  `avatar` varchar(255) DEFAULT NULL COMMENT '头像 URL',
  `status` tinyint unsigned DEFAULT 1 COMMENT '1正常 2冻结 3注销',
  `level` tinyint unsigned DEFAULT 1 COMMENT '客户等级 1普通 2VIP 3SVIP',
  `parent_agent_id` bigint unsigned DEFAULT NULL COMMENT '上级经销商 ID',
  `language` varchar(10) DEFAULT 'zh-CN' COMMENT '语言',
  `timezone` varchar(32) DEFAULT 'Asia/Shanghai' COMMENT '时区',
  `register_ip` varchar(45) DEFAULT NULL COMMENT '注册 IP（支持 IPv6）',
  `register_source` varchar(32) DEFAULT NULL COMMENT '注册来源（web/api/agent）',
  `last_login_at` datetime DEFAULT NULL COMMENT '最后登录时间',
  `last_login_ip` varchar(45) DEFAULT NULL COMMENT '最后登录 IP',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  `deleted_at` datetime DEFAULT NULL COMMENT '软删除时间',
  PRIMARY KEY (id),
  UNIQUE KEY uk_username (tenant_id, username),
  UNIQUE KEY uk_email (tenant_id, email),
  UNIQUE KEY uk_phone (tenant_id, phone),
  KEY idx_parent_agent (parent_agent_id),
  KEY idx_status (status),
  KEY idx_created_at (created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='客户主表';

DROP TABLE IF EXISTS `customer`.`customer_profiles`;
CREATE TABLE `customer`.`customer_profiles` (
  `id` bigint unsigned NOT NULL,
  `customer_id` bigint unsigned NOT NULL COMMENT '关联 customers.id（唯一）',
  `company` varchar(128) DEFAULT NULL COMMENT '公司名称',
  `department` varchar(64) DEFAULT NULL COMMENT '部门',
  `position` varchar(64) DEFAULT NULL COMMENT '职位',
  `country` varchar(64) DEFAULT NULL COMMENT '国家',
  `province` varchar(64) DEFAULT NULL COMMENT '省',
  `city` varchar(64) DEFAULT NULL COMMENT '市',
  `address` varchar(255) DEFAULT NULL COMMENT '详细地址',
  `zip_code` varchar(10) DEFAULT NULL COMMENT '邮编',
  `qq` varchar(20) DEFAULT NULL COMMENT 'QQ',
  `wechat` varchar(64) DEFAULT NULL COMMENT '微信',
  `telegram` varchar(64) DEFAULT NULL COMMENT 'Telegram',
  `gender` tinyint unsigned DEFAULT 0 COMMENT '0未知 1男 2女',
  `birthday` date DEFAULT NULL COMMENT '生日',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (id),UNIQUE KEY uk_customer (customer_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='客户资料扩展';

DROP TABLE IF EXISTS `customer`.`customer_realname`;
CREATE TABLE `customer`.`customer_realname` (
  `id` bigint unsigned NOT NULL,
  `customer_id` bigint unsigned NOT NULL COMMENT '关联客户',
  `type` tinyint unsigned DEFAULT 1 COMMENT '1个人 2企业',
  `name` varchar(64) NOT NULL COMMENT '姓名/企业名',
  `id_card_no` varchar(64) DEFAULT NULL COMMENT '身份证号（AES 加密）',
  `id_card_front` varchar(255) DEFAULT NULL COMMENT '身份证正面照',
  `id_card_back` varchar(255) DEFAULT NULL COMMENT '身份证反面照',
  `company_name` varchar(128) DEFAULT NULL COMMENT '企业名称',
  `company_license` varchar(255) DEFAULT NULL COMMENT '营业执照号',
  `company_license_img` varchar(255) DEFAULT NULL COMMENT '营业执照照片',
  `status` tinyint unsigned DEFAULT 1 COMMENT '1待审核 2通过 3拒绝',
  `reject_reason` varchar(255) DEFAULT NULL COMMENT '拒绝原因',
  `auditor_id` bigint unsigned DEFAULT NULL COMMENT '审核员 ID',
  `audited_at` datetime DEFAULT NULL COMMENT '审核时间',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (id),KEY idx_customer (customer_id),KEY idx_status (status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='实名认证';

DROP TABLE IF EXISTS `customer`.`oauth_accounts`;
CREATE TABLE `customer`.`oauth_accounts` (
  `id` bigint unsigned NOT NULL,
  `customer_id` bigint unsigned NOT NULL COMMENT '关联客户',
  `platform` varchar(32) NOT NULL COMMENT '平台（wechat/alipay/qq/github/google）',
  `openid` varchar(128) NOT NULL COMMENT '平台唯一 ID',
  `unionid` varchar(128) DEFAULT NULL COMMENT '联合 ID（微信）',
  `nickname` varchar(64) DEFAULT NULL COMMENT '平台昵称',
  `avatar` varchar(255) DEFAULT NULL COMMENT '平台头像',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (id),UNIQUE KEY uk_platform_openid (platform, openid),KEY idx_customer (customer_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='第三方登录绑定';

DROP TABLE IF EXISTS `customer`.`login_logs`;
CREATE TABLE `customer`.`login_logs` (
  `id` bigint unsigned NOT NULL,
  `customer_id` bigint unsigned DEFAULT NULL COMMENT '客户 ID（登录成功后）',
  `username` varchar(64) NOT NULL COMMENT '尝试登录的用户名',
  `ip` varchar(45) NOT NULL COMMENT '登录 IP',
  `user_agent` varchar(512) DEFAULT NULL COMMENT 'UA',
  `status` tinyint unsigned NOT NULL COMMENT '1成功 2失败',
  `fail_reason` varchar(64) DEFAULT NULL COMMENT '失败原因',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (id),KEY idx_customer (customer_id),KEY idx_ip (ip),KEY idx_created_at (created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='登录日志';

-- ============================================
-- 域: 产品域 (instance) - 5 张表
-- ============================================

DROP TABLE IF EXISTS `instance`.`instances`;
CREATE TABLE `instance`.`instances` (
  `id` bigint unsigned NOT NULL,
  `instance_no` varchar(32) NOT NULL COMMENT '实例编号（唯一，业务可读）',
  `tenant_id` bigint unsigned DEFAULT 0,
  `customer_id` bigint unsigned NOT NULL COMMENT '所属客户',
  `order_id` bigint unsigned NOT NULL COMMENT '来源订单',
  `order_item_id` bigint unsigned NOT NULL COMMENT '来源订单项',
  `product_id` bigint unsigned NOT NULL COMMENT '商品 ID（快照）',
  `product_name` varchar(128) NOT NULL COMMENT '商品名称（快照）',
  `module_type` varchar(32) NOT NULL COMMENT '交付模块',
  `config_snapshot` json NOT NULL COMMENT '配置快照',
  `status` tinyint unsigned DEFAULT 1 COMMENT '1待开通 2开通中 3运行中 4已停机 5已过期 6已删除',
  `upstream_provider_id` bigint unsigned DEFAULT NULL COMMENT '上游供应商',
  `upstream_instance_id` varchar(128) DEFAULT NULL COMMENT '上游实例 ID',
  `primary_ip` varchar(45) DEFAULT NULL COMMENT '缓存主IP',
  `subnet_mask` varchar(45) DEFAULT NULL COMMENT '缓存子网掩码',
  `gateway` varchar(45) DEFAULT NULL COMMENT '缓存网关',
  `operating_system` varchar(128) DEFAULT NULL COMMENT '缓存操作系统',
  `power_status` varchar(32) DEFAULT NULL COMMENT '缓存电源状态',
  `last_synced_at` datetime DEFAULT NULL COMMENT '上游状态同步时间',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `activated_at` datetime DEFAULT NULL COMMENT '开通时间',
  `expires_at` datetime DEFAULT NULL COMMENT '到期时间',
  `next_due_at` datetime DEFAULT NULL COMMENT '下次扣费时间（按需）',
  `suspended_at` datetime DEFAULT NULL COMMENT '停机时间',
  `deleted_at` datetime DEFAULT NULL COMMENT '软删除时间',
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (id),
  UNIQUE KEY uk_instance_no (instance_no),
  KEY idx_customer (customer_id, status),
  KEY idx_order (order_id),
  KEY idx_status (status),
  KEY idx_expires (expires_at),
  KEY idx_upstream (upstream_provider_id, upstream_instance_id),
  KEY idx_instance_search_ip (customer_id, primary_ip)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='产品实例';

DROP TABLE IF EXISTS `instance`.`instance_events`;
CREATE TABLE `instance`.`instance_events` (
  `id` bigint unsigned NOT NULL,
  `instance_id` bigint unsigned NOT NULL COMMENT '实例 ID',
  `event_type` varchar(32) NOT NULL COMMENT 'create/suspend/unsuspend/terminate/upgrade/renew/reboot/rebuild',
  `from_status` tinyint unsigned DEFAULT NULL COMMENT '原状态',
  `to_status` tinyint unsigned DEFAULT NULL COMMENT '新状态',
  `operator_type` tinyint unsigned NOT NULL COMMENT '1客户 2管理员 3系统 4定时任务',
  `operator_id` bigint unsigned DEFAULT NULL,
  `status` tinyint unsigned DEFAULT 1 COMMENT '1处理中 2成功 3失败',
  `upstream_request` json DEFAULT NULL COMMENT '上游请求参数',
  `upstream_response` json DEFAULT NULL COMMENT '上游响应',
  `error_msg` varchar(512) DEFAULT NULL COMMENT '失败原因',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `processed_at` datetime DEFAULT NULL,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (id),KEY idx_instance (instance_id, created_at),KEY idx_status (status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='实例事件';

DROP TABLE IF EXISTS `instance`.`upstream_providers`;
CREATE TABLE `instance`.`upstream_providers` (
  `id` bigint unsigned NOT NULL,
  `name` varchar(64) NOT NULL COMMENT '供应商名称（唯一）',
  `type` varchar(32) NOT NULL COMMENT '类型（aliyun/tencent/huawei/onpremise）',
  `endpoint` varchar(255) NOT NULL COMMENT 'API 端点',
  `api_key` varchar(255) NOT NULL COMMENT 'API Key（AES 加密）',
  `api_secret` varchar(255) NOT NULL COMMENT 'API Secret（AES 加密）',
  `config` json DEFAULT NULL COMMENT '额外配置',
  `status` tinyint unsigned DEFAULT 1 COMMENT '1启用 2禁用',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (id),UNIQUE KEY uk_name (name)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='上游供应商';

DROP TABLE IF EXISTS `instance`.`delivery_tasks`;
CREATE TABLE `instance`.`delivery_tasks` (
  `id` bigint unsigned NOT NULL,
  `instance_id` bigint unsigned NOT NULL COMMENT '实例 ID',
  `task_type` varchar(32) NOT NULL COMMENT 'create/suspend/terminate/upgrade/renew',
  `payload` json DEFAULT NULL COMMENT '任务参数',
  `status` tinyint unsigned DEFAULT 1 COMMENT '1待处理 2处理中 3成功 4失败 5已取消',
  `retry_count` int unsigned DEFAULT 0 COMMENT '重试次数',
  `max_retry` int unsigned DEFAULT 3 COMMENT '最大重试',
  `last_error` varchar(512) DEFAULT NULL COMMENT '最后错误',
  `next_retry_at` datetime DEFAULT NULL COMMENT '下次重试时间',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `processed_at` datetime DEFAULT NULL,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (id),KEY idx_status_next (status, next_retry_at),KEY idx_instance (instance_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='交付任务队列';

-- ============================================
-- 域: 订单域 (order) - 4 张表
-- ============================================

DROP TABLE IF EXISTS `order`.`carts`;
CREATE TABLE `order`.`carts` (
  `id` bigint unsigned NOT NULL,
  `customer_id` bigint unsigned NOT NULL COMMENT '客户 ID',
  `product_id` bigint unsigned NOT NULL COMMENT '商品 ID',
  `config_snapshot` json NOT NULL COMMENT '配置快照',
  `config_snapshot_hash` varchar(64) GENERATED ALWAYS AS (MD5(config_snapshot)) STORED COMMENT '配置快照哈希（唯一约束用）',
  `duration_months` int unsigned DEFAULT NULL COMMENT '购买周期',
  `qty` int unsigned DEFAULT 1 COMMENT '数量',
  `unit_price` bigint NOT NULL COMMENT '单价（分，快照）',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (id),UNIQUE KEY uk_customer_product (customer_id, product_id, config_snapshot_hash),KEY idx_customer (customer_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='购物车';

DROP TABLE IF EXISTS `order`.`orders`;
CREATE TABLE `order`.`orders` (
  `id` bigint unsigned NOT NULL,
  `order_no` varchar(32) NOT NULL COMMENT '订单号（唯一，业务可读）',
  `tenant_id` bigint unsigned DEFAULT 0,
  `customer_id` bigint unsigned NOT NULL COMMENT '客户 ID',
  `parent_agent_id` bigint unsigned DEFAULT NULL COMMENT '下单时上级经销商（快照）',
  `total_amount` bigint DEFAULT 0 COMMENT '商品总额（分）',
  `discount_amount` bigint DEFAULT 0 COMMENT '优惠金额（分）',
  `setup_fee` bigint DEFAULT 0 COMMENT '安装费合计（分）',
  `pay_amount` bigint DEFAULT 0 COMMENT '实付金额（分）',
  `coupon_id` bigint unsigned DEFAULT NULL COMMENT '使用的优惠券',
  `coupon_amount` bigint DEFAULT 0 COMMENT '优惠券抵扣（分）',
  `balance_amount` bigint DEFAULT 0 COMMENT '余额支付（分）',
  `status` tinyint unsigned DEFAULT 1 COMMENT '1待支付 2已支付 3已取消 4已退款 5部分退款 6已完成',
  `pay_type` tinyint unsigned DEFAULT NULL COMMENT '支付方式（快照）',
  `paid_at` datetime DEFAULT NULL COMMENT '支付时间',
  `cancelled_at` datetime DEFAULT NULL COMMENT '取消时间',
  `cancel_reason` varchar(255) DEFAULT NULL COMMENT '取消原因',
  `customer_remark` varchar(255) DEFAULT NULL COMMENT '客户备注',
  `admin_remark` varchar(255) DEFAULT NULL COMMENT '管理员备注',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (id),
  UNIQUE KEY uk_order_no (order_no),
  KEY idx_customer (customer_id, status),
  KEY idx_status (status),
  KEY idx_created_at (created_at),
  KEY idx_paid_at (paid_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='订单主表';

DROP TABLE IF EXISTS `order`.`order_items`;
CREATE TABLE `order`.`order_items` (
  `id` bigint unsigned NOT NULL,
  `order_id` bigint unsigned NOT NULL COMMENT '订单 ID',
  `product_id` bigint unsigned NOT NULL COMMENT '商品 ID（快照）',
  `product_name` varchar(128) NOT NULL COMMENT '商品名称（快照）',
  `module_type` varchar(32) NOT NULL COMMENT '交付模块（快照）',
  `config_snapshot` json NOT NULL COMMENT '订单配置快照：configOptions/hostname/billingCycle/passwordCiphertext；固定商品为{}',
  `duration_months` int unsigned DEFAULT NULL COMMENT '周期',
  `qty` int unsigned DEFAULT 1 COMMENT '数量',
  `unit_price` bigint NOT NULL COMMENT '单价（分）',
  `subtotal` bigint NOT NULL COMMENT '小计（分）',
  `setup_fee` bigint DEFAULT 0 COMMENT '安装费（分）',
  `instance_id` bigint unsigned DEFAULT NULL COMMENT '交付后生成的实例 ID',
  `delivery_status` tinyint unsigned DEFAULT 0 COMMENT '0待交付 1交付中 2已交付 3交付失败',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (id),KEY idx_order (order_id),KEY idx_instance (instance_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='订单明细';

DROP TABLE IF EXISTS `order`.`order_events`;
CREATE TABLE `order`.`order_events` (
  `id` bigint unsigned NOT NULL,
  `order_id` bigint unsigned NOT NULL COMMENT '订单 ID',
  `from_status` tinyint unsigned DEFAULT NULL COMMENT '原状态',
  `to_status` tinyint unsigned NOT NULL COMMENT '新状态',
  `operator_type` tinyint unsigned NOT NULL COMMENT '1客户 2管理员 3系统',
  `operator_id` bigint unsigned DEFAULT NULL COMMENT '操作人 ID',
  `remark` varchar(255) DEFAULT NULL COMMENT '备注',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (id),KEY idx_order (order_id, created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='订单状态机事件';

-- ============================================
-- 域: 支付域 (payment) - 4 张表
-- ============================================

DROP TABLE IF EXISTS `payment`.`payments`;
CREATE TABLE `payment`.`payments` (
  `id` bigint unsigned NOT NULL,
  `payment_no` varchar(32) NOT NULL COMMENT '支付单号（唯一）',
  `biz_type` varchar(16) NOT NULL COMMENT '业务类型（order/recharge）',
  `biz_id` bigint unsigned NOT NULL COMMENT '订单或充值单 ID',
  `order_id` bigint unsigned DEFAULT NULL COMMENT '关联订单（充值为空）',
  `customer_id` bigint unsigned NOT NULL COMMENT '客户 ID',
  `amount` decimal(10,2) NOT NULL COMMENT '支付金额（元，两位小数）',
  `amount_fen` bigint NOT NULL COMMENT '支付金额（分，渠道校验权威值）',
  `currency` char(3) NOT NULL DEFAULT 'CNY',
  `channel` varchar(32) NOT NULL COMMENT '渠道（alipay/wechat）',
  `out_trade_no` varchar(64) NOT NULL COMMENT '提交渠道的商户订单号',
  `transaction_id` varchar(128) DEFAULT NULL COMMENT '渠道交易号',
  `channel_trade_no` varchar(128) DEFAULT NULL COMMENT '旧第三方交易号（兼容保留）',
  `status` varchar(16) NOT NULL DEFAULT 'pending' COMMENT 'pending/paid/closed/failed/refunded',
  `notify_raw` text DEFAULT NULL COMMENT '脱敏后的回调摘要，仅调试排障',
  `paid_at` datetime DEFAULT NULL COMMENT '支付成功时间',
  `closed_at` datetime DEFAULT NULL COMMENT '关闭时间',
  `expire_at` datetime NOT NULL COMMENT '支付过期时间',
  `paid_ip` varchar(45) DEFAULT NULL COMMENT '支付 IP',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (id),UNIQUE KEY uk_payment_no (payment_no),UNIQUE KEY uk_out_trade_no (out_trade_no),UNIQUE KEY uk_biz (biz_type,biz_id),KEY idx_order (order_id),KEY idx_customer (customer_id),KEY idx_status (status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='支付单';

DROP TABLE IF EXISTS `payment`.`payment_callbacks`;
CREATE TABLE `payment`.`payment_callbacks` (
  `id` bigint unsigned NOT NULL,
  `payment_id` bigint unsigned NOT NULL COMMENT '支付单 ID',
  `channel` varchar(32) NOT NULL COMMENT '渠道',
  `raw_data` json NOT NULL COMMENT '原始回调数据',
  `verified` tinyint unsigned DEFAULT 0 COMMENT '验签 0失败 1成功',
  `processed` tinyint unsigned DEFAULT 0 COMMENT '处理状态',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (id),KEY idx_payment (payment_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='支付回调记录';

DROP TABLE IF EXISTS `payment`.`refunds`;
CREATE TABLE `payment`.`refunds` (
  `id` bigint unsigned NOT NULL,
  `refund_no` varchar(32) NOT NULL COMMENT '退款单号（唯一）',
  `order_id` bigint unsigned NOT NULL COMMENT '关联订单',
  `payment_id` bigint unsigned NOT NULL COMMENT '原支付单',
  `customer_id` bigint unsigned NOT NULL COMMENT '客户 ID',
  `amount` bigint NOT NULL COMMENT '退款金额（分）',
  `reason` varchar(255) NOT NULL COMMENT '退款原因',
  `status` tinyint unsigned DEFAULT 1 COMMENT '1申请中 2审核通过 3审核拒绝 4退款中 5已退款 6退款失败',
  `refund_channel` varchar(32) DEFAULT NULL COMMENT '退款渠道（原路退回）',
  `channel_refund_no` varchar(128) DEFAULT NULL COMMENT '第三方退款号',
  `auditor_id` bigint unsigned DEFAULT NULL COMMENT '审核员',
  `audit_remark` varchar(255) DEFAULT NULL COMMENT '审核备注',
  `processed_at` datetime DEFAULT NULL COMMENT '退款完成时间',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (id),UNIQUE KEY uk_refund_no (refund_no),KEY idx_order (order_id),KEY idx_status (status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='退款单';

DROP TABLE IF EXISTS `payment`.`recharge_orders`;
CREATE TABLE `payment`.`recharge_orders` (
  `id` bigint unsigned NOT NULL,
  `recharge_no` varchar(32) NOT NULL COMMENT '充值单号（唯一）',
  `customer_id` bigint unsigned NOT NULL,
  `amount` bigint NOT NULL COMMENT '充值金额（分）',
  `bonus` bigint DEFAULT 0 COMMENT '赠送金额（分）',
  `channel` varchar(32) NOT NULL COMMENT '支付渠道',
  `status` tinyint unsigned DEFAULT 1 COMMENT '1待支付 2成功 3失败 4已关闭',
  `paid_at` datetime DEFAULT NULL,
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (id),UNIQUE KEY uk_recharge_no (recharge_no),KEY idx_customer (customer_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='充值单';

-- ============================================
-- 域: 商品域 (product) - 5 张表
-- ============================================

DROP TABLE IF EXISTS `product`.`product_groups`;
CREATE TABLE `product`.`product_groups` (
  `id` bigint unsigned NOT NULL,
  `tenant_id` bigint unsigned DEFAULT 0,
  `parent_id` bigint unsigned DEFAULT 0 COMMENT '父分组 ID（0=顶级）',
  `name` varchar(64) NOT NULL COMMENT '分组名称',
  `icon` varchar(255) DEFAULT NULL COMMENT '图标',
  `sort` int unsigned DEFAULT 0 COMMENT '排序（小在前）',
  `status` tinyint unsigned DEFAULT 1 COMMENT '1启用 2禁用',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (id),KEY idx_parent (parent_id),KEY idx_sort (sort)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='商品分组';

DROP TABLE IF EXISTS `product`.`products`;
CREATE TABLE `product`.`products` (
  `id` bigint unsigned NOT NULL,
  `tenant_id` bigint unsigned DEFAULT 0,
  `group_id` bigint unsigned NOT NULL COMMENT '所属分组',
  `name` varchar(128) NOT NULL COMMENT '商品名称',
  `module_type` varchar(32) NOT NULL COMMENT '交付模块类型（cloud/dcim/ssl/sms/domain...）',
  `pay_type` tinyint unsigned DEFAULT 1 COMMENT '1包年包月 2按需付费 3一次性',
  `stock` int DEFAULT NULL COMMENT '库存（NULL=不限）',
  `sold_count` int unsigned DEFAULT 0 COMMENT '已售数量',
  `status` tinyint unsigned DEFAULT 1 COMMENT '1上架 2下架 3维护中',
  `hidden` tinyint unsigned DEFAULT 0 COMMENT '0显示 1隐藏（仅后台可见）',
  `recommended` tinyint unsigned DEFAULT 0 COMMENT '0否 1推荐',
  `sort` int unsigned DEFAULT 0 COMMENT '排序',
  `thumbnail` varchar(255) DEFAULT NULL COMMENT '缩略图',
  `images` json DEFAULT NULL COMMENT '轮播图数组',
  `summary` varchar(255) DEFAULT NULL COMMENT '简介',
  `description` text DEFAULT NULL COMMENT '详细描述（富文本）',
  `features` json DEFAULT NULL COMMENT '特性列表 [{icon,title,desc}]',
  `faq` json DEFAULT NULL COMMENT 'FAQ [{q,a}]',
  `seo_title` varchar(128) DEFAULT NULL COMMENT 'SEO 标题',
  `seo_keywords` varchar(255) DEFAULT NULL COMMENT 'SEO 关键词',
  `seo_description` varchar(255) DEFAULT NULL COMMENT 'SEO 描述',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (id),KEY idx_group (group_id),KEY idx_module (module_type),KEY idx_status (status, hidden),KEY idx_sort (sort)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='商品主表';

DROP TABLE IF EXISTS `product`.`product_configs`;
CREATE TABLE `product`.`product_configs` (
  `id` bigint unsigned NOT NULL,
  `product_id` bigint unsigned NOT NULL COMMENT '关联商品',
  `name` varchar(64) NOT NULL COMMENT '配置项名称（如"操作系统"）',
  `field_key` varchar(64) NOT NULL COMMENT '字段键名（如 os）',
  `type` varchar(16) NOT NULL COMMENT 'select/input/textarea/switch/radio/checkbox',
  `options` json DEFAULT NULL COMMENT '选项 [{label,value,price_affect}]',
  `placeholder` varchar(128) DEFAULT NULL COMMENT '占位提示',
  `default_value` varchar(255) DEFAULT NULL COMMENT '默认值',
  `required` tinyint unsigned DEFAULT 0 COMMENT '是否必填',
  `sort` int unsigned DEFAULT 0 COMMENT '排序',
  `status` tinyint unsigned DEFAULT 1 COMMENT '1启用 2禁用',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (id),KEY idx_product (product_id, sort)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='商品配置项';

DROP TABLE IF EXISTS `product`.`product_prices`;
CREATE TABLE `product`.`product_prices` (
  `id` bigint unsigned NOT NULL,
  `product_id` bigint unsigned NOT NULL COMMENT '关联商品',
  `config_snapshot` json DEFAULT NULL COMMENT '配置组合快照 {os:"centos7",cpu:"2"}',
  `duration_months` int unsigned DEFAULT NULL COMMENT '周期（月），NULL=按需/一次性',
  `price` bigint NOT NULL COMMENT '售价（分）',
  `cost` bigint DEFAULT 0 COMMENT '成本价（分）',
  `setup_fee` bigint DEFAULT 0 COMMENT '安装费（分）',
  `ondemand_price` bigint DEFAULT NULL COMMENT '按需单价（分/小时）',
  `agent_price` json DEFAULT NULL COMMENT '经销商价格 {level1:price, level2:price}',
  `status` tinyint unsigned DEFAULT 1 COMMENT '1启用 2禁用',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (id),KEY idx_product_duration (product_id, duration_months)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='商品价格';

DROP TABLE IF EXISTS `product`.`delivery_modules`;
CREATE TABLE `product`.`delivery_modules` (
  `id` bigint unsigned NOT NULL,
  `name` varchar(64) NOT NULL COMMENT '模块名称（唯一）',
  `type` varchar(32) NOT NULL COMMENT '类型（cloud/dcim/ssl/sms）',
  `endpoint` varchar(255) NOT NULL COMMENT 'gRPC 端点或适配器类名',
  `version` varchar(16) DEFAULT 1.0 COMMENT '模块版本',
  `config_schema` json DEFAULT NULL COMMENT '配置参数 JSON Schema',
  `status` tinyint unsigned DEFAULT 1 COMMENT '1启用 2禁用',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (id),UNIQUE KEY uk_name (name)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='交付模块注册';

-- ============================================
-- 域: 运营域 (system) - 8 张表
-- ============================================

DROP TABLE IF EXISTS `system`.`admins`;
CREATE TABLE `system`.`admins` (
  `id` bigint unsigned NOT NULL,
  `username` varchar(64) NOT NULL COMMENT '用户名（唯一）',
  `password_hash` varchar(255) NOT NULL,
  `real_name` varchar(64) NOT NULL,
  `email` varchar(128) DEFAULT NULL,
  `phone` varchar(20) DEFAULT NULL,
  `avatar` varchar(255) DEFAULT NULL,
  `status` tinyint unsigned DEFAULT 1 COMMENT '1正常 2禁用',
  `is_super` tinyint unsigned DEFAULT 0 COMMENT '是否超管',
  `two_factor_enabled` tinyint unsigned DEFAULT 0 COMMENT '2FA',
  `two_factor_secret` varchar(255) DEFAULT NULL COMMENT '2FA 密钥（加密）',
  `last_login_at` datetime DEFAULT NULL,
  `last_login_ip` varchar(45) DEFAULT NULL,
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (id),UNIQUE KEY uk_username (username)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='管理员';

DROP TABLE IF EXISTS `system`.`roles`;
CREATE TABLE `system`.`roles` (
  `id` bigint unsigned NOT NULL,
  `name` varchar(64) NOT NULL COMMENT '角色名（唯一）',
  `description` varchar(255) DEFAULT NULL,
  `status` tinyint unsigned DEFAULT 1,
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (id),UNIQUE KEY uk_name (name)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='角色';

DROP TABLE IF EXISTS `system`.`permissions`;
CREATE TABLE `system`.`permissions` (
  `id` bigint unsigned NOT NULL,
  `code` varchar(128) NOT NULL COMMENT '权限码（唯一，如 order:refund）',
  `name` varchar(64) NOT NULL COMMENT '权限名称',
  `module` varchar(32) NOT NULL COMMENT '所属模块',
  `parent_id` bigint unsigned DEFAULT 0 COMMENT '父权限',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (id),UNIQUE KEY uk_code (code),KEY idx_module (module)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='权限';

DROP TABLE IF EXISTS `system`.`admin_roles`;
CREATE TABLE `system`.`admin_roles` (
  `admin_id` bigint unsigned NOT NULL,
  `role_id` bigint unsigned NOT NULL,
  PRIMARY KEY (admin_id, role_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='管理员-角色关联';

DROP TABLE IF EXISTS `system`.`role_permissions`;
CREATE TABLE `system`.`role_permissions` (
  `role_id` bigint unsigned NOT NULL,
  `permission_id` bigint unsigned NOT NULL,
  PRIMARY KEY (role_id, permission_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='角色-权限关联';

DROP TABLE IF EXISTS `system`.`announcements`;
CREATE TABLE `system`.`announcements` (
  `id` bigint unsigned NOT NULL,
  `tenant_id` bigint unsigned DEFAULT 0,
  `title` varchar(128) NOT NULL,
  `content` longtext NOT NULL COMMENT '富文本',
  `type` tinyint unsigned DEFAULT 1 COMMENT '1公告 2新闻 3活动',
  `status` tinyint unsigned DEFAULT 1 COMMENT '1草稿 2已发布 3已下线',
  `view_count` int unsigned DEFAULT 0 COMMENT '浏览量',
  `sort` int unsigned DEFAULT 0,
  `published_at` datetime DEFAULT NULL,
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (id),KEY idx_status_type (status, type, published_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='公告';

DROP TABLE IF EXISTS `system`.`audit_logs`;
CREATE TABLE `system`.`audit_logs` (
  `id` bigint unsigned NOT NULL,
  `operator_type` tinyint unsigned NOT NULL COMMENT '1管理员 2客户 3系统',
  `operator_id` bigint unsigned NOT NULL,
  `action` varchar(64) NOT NULL COMMENT '操作（create/update/delete/refund）',
  `target_type` varchar(32) NOT NULL COMMENT '操作对象类型（order/customer/product）',
  `target_id` bigint unsigned DEFAULT NULL,
  `ip` varchar(45) DEFAULT NULL,
  `user_agent` varchar(512) DEFAULT NULL,
  `detail` json DEFAULT NULL COMMENT '变更详情 {before, after}',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (id),KEY idx_operator (operator_type, operator_id),KEY idx_target (target_type, target_id),KEY idx_created_at (created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='操作审计';

DROP TABLE IF EXISTS `system`.`notify_logs`;
CREATE TABLE `system`.`notify_logs` (
  `id` bigint unsigned NOT NULL,
  `scene` varchar(32) NOT NULL,
  `channel` varchar(16) NOT NULL,
  `target` varchar(255) NOT NULL COMMENT '脱敏后的手机号或邮箱',
  `template_id` bigint unsigned DEFAULT NULL,
  `content_snapshot` text NOT NULL,
  `status` varchar(16) NOT NULL COMMENT 'success|failed',
  `error` varchar(1024) NOT NULL DEFAULT '',
  `request_id` varchar(128) NOT NULL DEFAULT '' COMMENT '阿里云 BizId',
  `business_id` varchar(128) NOT NULL DEFAULT '' COMMENT '业务对象ID，用于到期提醒判重',
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  KEY `idx_notify_log_query` (`scene`,`channel`,`status`,`created_at`),
  KEY `idx_notify_log_dedupe` (`scene`,`business_id`,`status`),
  KEY `idx_notify_log_template` (`template_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='通知发送记录';

DROP TABLE IF EXISTS `system`.`notification_templates`;
CREATE TABLE `system`.`notification_templates` (
  `id` bigint unsigned NOT NULL,
  `scene` varchar(32) NOT NULL,
  `channel` varchar(16) NOT NULL DEFAULT 'both' COMMENT 'sms|email|both',
  `title` varchar(255) NOT NULL DEFAULT '',
  `content` text NOT NULL COMMENT '支持 {{变量}} 占位',
  `enabled` tinyint(1) NOT NULL DEFAULT 1,
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_notify_template_scene` (`scene`),
  KEY `idx_notify_template_enabled` (`enabled`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='通知模板';

DROP TABLE IF EXISTS `system`.`system_configs`;
CREATE TABLE `system`.`system_configs` (
  `id` bigint unsigned NOT NULL,
  `key` varchar(128) NOT NULL COMMENT '配置键（唯一）',
  `value` text DEFAULT NULL COMMENT '配置值',
  `description` varchar(255) DEFAULT NULL,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (id),UNIQUE KEY uk_key (`key`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='系统配置';

-- ============================================
-- 域: 工单域 (ticket) - Phase 1 基础闭环
-- ============================================

DROP TABLE IF EXISTS `ticket`.`ticket_logs`;
DROP TABLE IF EXISTS `ticket`.`ticket_notifications`;
DROP TABLE IF EXISTS `ticket`.`ticket_reads`;
DROP TABLE IF EXISTS `ticket`.`ticket_forward`;
DROP TABLE IF EXISTS `ticket`.`ticket_prereply`;
DROP TABLE IF EXISTS `ticket`.`ticket_type_admin_link`;
DROP TABLE IF EXISTS `ticket`.`ticket_notes`;
DROP TABLE IF EXISTS `ticket`.`ticket_reply`;
DROP TABLE IF EXISTS `ticket`.`ticket_host_link`;
DROP TABLE IF EXISTS `ticket`.`ticket`;
DROP TABLE IF EXISTS `ticket`.`ticket_type`;
DROP TABLE IF EXISTS `ticket`.`ticket_status`;

CREATE TABLE `ticket`.`ticket_status` (
  `id` bigint unsigned NOT NULL, `name` varchar(32) NOT NULL, `color` varchar(16) NOT NULL,
  `status` tinyint(1) NOT NULL DEFAULT 0 COMMENT '1完结 0未完结', `is_default` tinyint(1) NOT NULL DEFAULT 0,
  `create_time` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP, `update_time` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`), KEY `idx_default` (`is_default`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='工单状态';

CREATE TABLE `ticket`.`ticket_type` (
  `id` bigint unsigned NOT NULL, `name` varchar(64) NOT NULL,
  `create_time` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP, `update_time` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`), UNIQUE KEY `uk_name` (`name`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='工单类型';

CREATE TABLE `ticket`.`ticket` (
  `id` bigint unsigned NOT NULL, `ticket_num` varchar(16) NOT NULL, `client_id` bigint unsigned NOT NULL,
  `title` varchar(128) NOT NULL, `ticket_type_id` bigint unsigned NOT NULL, `content` text NOT NULL, `status` bigint unsigned NOT NULL,
  `attachment` json DEFAULT NULL, `last_reply_time` datetime NOT NULL, `post_time` datetime NOT NULL,
  `admin_id` bigint unsigned DEFAULT NULL, `last_reply_admin_id` bigint unsigned DEFAULT NULL,
  `create_time` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP, `update_time` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`), UNIQUE KEY `uk_ticket_num` (`ticket_num`), KEY `idx_client_status` (`client_id`,`status`),
  KEY `idx_admin_status` (`admin_id`,`status`), KEY `idx_update` (`update_time`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='工单';

CREATE TABLE `ticket`.`ticket_host_link` (
  `id` bigint unsigned NOT NULL, `ticket_id` bigint unsigned NOT NULL, `host_id` bigint unsigned NOT NULL,
  PRIMARY KEY (`id`), UNIQUE KEY `uk_ticket_host` (`ticket_id`,`host_id`), KEY `idx_host` (`host_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='工单关联产品';

CREATE TABLE `ticket`.`ticket_reply` (
  `id` bigint unsigned NOT NULL, `ticket_id` bigint unsigned NOT NULL, `type` varchar(16) NOT NULL,
  `rel_id` bigint unsigned NOT NULL, `content` text NOT NULL, `attachment` json DEFAULT NULL,
  `quote_reply_id` bigint unsigned DEFAULT NULL, `create_time` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`), KEY `idx_ticket_id` (`ticket_id`,`id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='工单回复';

CREATE TABLE `ticket`.`ticket_notes` (
  `id` bigint unsigned NOT NULL, `ticket_id` bigint unsigned NOT NULL, `content` text NOT NULL,
  `admin_id` bigint unsigned NOT NULL, `create_time` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`), KEY `idx_ticket` (`ticket_id`,`create_time`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='内部备注';

CREATE TABLE `ticket`.`ticket_type_admin_link` (
  `ticket_type_id` bigint unsigned NOT NULL, `admin_id` bigint unsigned NOT NULL,
  PRIMARY KEY (`ticket_type_id`,`admin_id`), KEY `idx_admin` (`admin_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='工单类型处理人';

CREATE TABLE `ticket`.`ticket_prereply` (
  `id` bigint unsigned NOT NULL, `content` text NOT NULL, `sort` int NOT NULL DEFAULT 0,
  `create_time` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP, `update_time` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`), KEY `idx_sort` (`sort`,`id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='预设回复';

CREATE TABLE `ticket`.`ticket_forward` (
  `id` bigint unsigned NOT NULL, `ticket_id` bigint unsigned NOT NULL, `admin_id` bigint unsigned NOT NULL,
  `forward_admin_id` bigint unsigned DEFAULT NULL, `ticket_type_id` bigint unsigned DEFAULT NULL,
  `notes` varchar(255) NOT NULL DEFAULT '', `create_time` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`), KEY `idx_ticket` (`ticket_id`,`create_time`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='工单转交记录';

CREATE TABLE `ticket`.`ticket_reads` (
  `ticket_id` bigint unsigned NOT NULL, `actor_type` varchar(16) NOT NULL, `actor_id` bigint unsigned NOT NULL,
  `last_reply_id` bigint unsigned NOT NULL DEFAULT 0, `update_time` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`ticket_id`,`actor_type`,`actor_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='操作者已读游标';

CREATE TABLE `ticket`.`ticket_notifications` (
  `id` bigint unsigned NOT NULL, `ticket_id` bigint unsigned NOT NULL, `recipient_type` varchar(16) NOT NULL,
  `recipient_id` bigint unsigned NOT NULL, `kind` varchar(32) NOT NULL, `title` varchar(128) NOT NULL,
  `content` varchar(255) NOT NULL, `read_at` datetime DEFAULT NULL, `create_time` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`), KEY `idx_recipient` (`recipient_type`,`recipient_id`,`read_at`,`create_time`), KEY `idx_ticket` (`ticket_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='工单站内通知';

CREATE TABLE `ticket`.`ticket_logs` (
  `id` bigint unsigned NOT NULL, `ticket_id` bigint unsigned NOT NULL, `actor_type` varchar(16) NOT NULL,
  `actor_id` bigint unsigned NOT NULL, `action` varchar(32) NOT NULL, `detail` varchar(255) NOT NULL,
  `create_time` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`), KEY `idx_ticket` (`ticket_id`,`create_time`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='工单操作日志';

INSERT IGNORE INTO `ticket`.`ticket_status` (`id`,`name`,`color`,`status`,`is_default`) VALUES
  (1,'待接单','#779500',0,1),(2,'用户已回复','#ff6600',0,0),(3,'已回复','#000000',1,0),
  (4,'已关闭','#888888',1,0),(5,'处理中','#cc0000',0,0);
INSERT IGNORE INTO `ticket`.`ticket_type` (`id`,`name`) VALUES (1,'技术支持'),(2,'财务问题'),(3,'售前咨询');

SET FOREIGN_KEY_CHECKS = 1;
-- ============================================
-- 营销域（marketing）8张表
-- ============================================

-- 10.1 invitation_codes 邀请码表
DROP TABLE IF EXISTS `marketing`.`invitation_codes`;
CREATE TABLE `marketing`.`invitation_codes` (
  id BIGINT UNSIGNED NOT NULL COMMENT '雪花ID',
  user_id BIGINT UNSIGNED NOT NULL COMMENT '所属用户ID',
  code VARCHAR(16) NOT NULL COMMENT '邀请码（唯一）',
  custom_code VARCHAR(16) DEFAULT NULL COMMENT '自定义邀请码',
  invite_count INT UNSIGNED NOT NULL DEFAULT 0 COMMENT '已邀请人数',
  level1_count INT UNSIGNED NOT NULL DEFAULT 0 COMMENT '一级邀请人数',
  level2_count INT UNSIGNED NOT NULL DEFAULT 0 COMMENT '二级邀请人数',
  first_pay_count INT UNSIGNED NOT NULL DEFAULT 0 COMMENT '首购人数',
  total_commission DECIMAL(12,2) NOT NULL DEFAULT 0.00 COMMENT '累计佣金',
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (id),
  UNIQUE KEY uk_code (code),
  UNIQUE KEY uk_custom_code (custom_code),
  KEY idx_user_id (user_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='邀请码表';

-- 10.2 invitation_relations 邀请关系表
DROP TABLE IF EXISTS `marketing`.`invitation_relations`;
CREATE TABLE `marketing`.`invitation_relations` (
  id BIGINT UNSIGNED NOT NULL COMMENT '雪花ID',
  inviter_id BIGINT UNSIGNED NOT NULL COMMENT '邀请人用户ID',
  invitee_id BIGINT UNSIGNED NOT NULL COMMENT '被邀请人用户ID',
  level TINYINT UNSIGNED NOT NULL DEFAULT 1 COMMENT '关系层级：1一级 2二级',
  parent_chain VARCHAR(255) DEFAULT NULL COMMENT '上级关系链',
  invite_code VARCHAR(16) NOT NULL COMMENT '使用的邀请码',
  register_ip VARCHAR(45) DEFAULT NULL COMMENT '注册IP',
  device_id VARCHAR(64) DEFAULT NULL COMMENT '设备ID（风控）',
  status TINYINT UNSIGNED NOT NULL DEFAULT 0 COMMENT '0待首购 1已首购 2无效',
  first_pay_at DATETIME DEFAULT NULL COMMENT '首购时间',
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (id),
  UNIQUE KEY uk_invitee_level (invitee_id, level),
  KEY idx_inviter_level (inviter_id, level),
  KEY idx_status (status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='邀请关系表';

-- 10.3 invitation_rewards 邀请奖励记录表
DROP TABLE IF EXISTS `marketing`.`invitation_rewards`;
CREATE TABLE `marketing`.`invitation_rewards` (
  id BIGINT UNSIGNED NOT NULL COMMENT '雪花ID',
  inviter_id BIGINT UNSIGNED NOT NULL COMMENT '邀请人ID',
  invitee_id BIGINT UNSIGNED NOT NULL COMMENT '被邀请人ID',
  level TINYINT UNSIGNED NOT NULL DEFAULT 1 COMMENT '佣金层级：1一级 2二级',
  reward_type TINYINT UNSIGNED NOT NULL COMMENT '1首购佣金 2续费佣金 3阶梯奖励',
  order_id BIGINT UNSIGNED DEFAULT NULL COMMENT '关联订单ID',
  amount DECIMAL(12,2) NOT NULL DEFAULT 0.00 COMMENT '奖励金额',
  status TINYINT UNSIGNED NOT NULL DEFAULT 0 COMMENT '0待结算 1可提现 2已提现 3已取消',
  settle_at DATETIME DEFAULT NULL COMMENT '结算时间',
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (id),
  KEY idx_inviter_status (inviter_id, status),
  KEY idx_order (order_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='邀请奖励记录表';

-- 10.4 coupons 优惠券模板表（M4：金额统一 bigint 分，服务端计算）
DROP TABLE IF EXISTS `marketing`.`coupons`;
CREATE TABLE `marketing`.`coupons` (
  id BIGINT UNSIGNED NOT NULL COMMENT '雪花ID',
  name VARCHAR(64) NOT NULL COMMENT '券名称',
  type TINYINT UNSIGNED NOT NULL COMMENT '1满减 2折扣',
  face_value BIGINT NOT NULL DEFAULT 0 COMMENT '满减面额（分，满减券用）',
  discount_rate DECIMAL(5,4) NOT NULL DEFAULT 0 COMMENT '折扣率（0.9=9折，折扣券用）',
  max_discount BIGINT NOT NULL DEFAULT 0 COMMENT '最高优惠（分，折扣券用，0=不限）',
  min_amount BIGINT NOT NULL DEFAULT 0 COMMENT '使用门槛（分）',
  total_count INT NOT NULL DEFAULT -1 COMMENT '发放总量（-1不限）',
  claimed_count INT UNSIGNED NOT NULL DEFAULT 0 COMMENT '已领取数量',
  used_count INT UNSIGNED NOT NULL DEFAULT 0 COMMENT '已核销数量',
  per_user_limit INT UNSIGNED NOT NULL DEFAULT 1 COMMENT '每人限领数量',
  claim_code VARCHAR(32) DEFAULT NULL COMMENT '领取码（可空，唯一）',
  valid_days INT UNSIGNED NOT NULL DEFAULT 30 COMMENT '领取后有效天数',
  end_at DATETIME DEFAULT NULL COMMENT '发放截止时间',
  status TINYINT UNSIGNED NOT NULL DEFAULT 1 COMMENT '0停用 1启用',
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (id),
  UNIQUE KEY uk_claim_code (claim_code),
  KEY idx_status (status),
  KEY idx_type (type)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='优惠券模板表';

-- 10.5 user_coupons 用户优惠券表（M4）
DROP TABLE IF EXISTS `marketing`.`user_coupons`;
CREATE TABLE `marketing`.`user_coupons` (
  id BIGINT UNSIGNED NOT NULL COMMENT '雪花ID',
  user_id BIGINT UNSIGNED NOT NULL COMMENT '用户ID',
  coupon_id BIGINT UNSIGNED NOT NULL COMMENT '优惠券模板ID',
  code VARCHAR(32) NOT NULL COMMENT '券码（唯一）',
  source TINYINT UNSIGNED NOT NULL DEFAULT 1 COMMENT '1自主领取 2定向发放 3领取码',
  status TINYINT UNSIGNED NOT NULL DEFAULT 0 COMMENT '0未使用 1已使用 2已过期 3已作废',
  order_id BIGINT UNSIGNED DEFAULT NULL COMMENT '核销订单ID',
  used_at DATETIME DEFAULT NULL COMMENT '核销时间',
  expire_at DATETIME NOT NULL COMMENT '过期时间',
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (id),
  UNIQUE KEY uk_code (code),
  KEY idx_user_status (user_id, status),
  KEY idx_coupon (coupon_id),
  KEY idx_order (order_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='用户优惠券表';

-- 10.6 promotion_activities 营销活动表
DROP TABLE IF EXISTS `marketing`.`promotion_activities`;
CREATE TABLE `marketing`.`promotion_activities` (
  id BIGINT UNSIGNED NOT NULL COMMENT '雪花ID',
  name VARCHAR(64) NOT NULL COMMENT '活动名称',
  type TINYINT UNSIGNED NOT NULL COMMENT '1限时折扣 2拼团 3签到 4积分商城',
  description TEXT DEFAULT NULL COMMENT '活动描述',
  config JSON DEFAULT NULL COMMENT '活动配置',
  product_ids JSON DEFAULT NULL COMMENT '参与商品ID',
  start_at DATETIME NOT NULL COMMENT '开始时间',
  end_at DATETIME NOT NULL COMMENT '结束时间',
  status TINYINT UNSIGNED NOT NULL DEFAULT 0 COMMENT '0未开始 1进行中 2已结束 3已取消',
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (id),
  KEY idx_type_status (type, status),
  KEY idx_time (start_at, end_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='营销活动表';

-- 10.7 points_logs 积分流水表
DROP TABLE IF EXISTS `marketing`.`points_logs`;
CREATE TABLE `marketing`.`points_logs` (
  id BIGINT UNSIGNED NOT NULL COMMENT '雪花ID',
  user_id BIGINT UNSIGNED NOT NULL COMMENT '用户ID',
  change_type TINYINT UNSIGNED NOT NULL COMMENT '1注册 2登录 3购买 4邀请 5消费抵扣 6兑换',
  points INT NOT NULL COMMENT '积分变动（正增加负减少）',
  balance INT UNSIGNED NOT NULL COMMENT '变动后余额',
  relation_id BIGINT UNSIGNED DEFAULT NULL COMMENT '关联ID',
  remark VARCHAR(128) DEFAULT NULL COMMENT '备注',
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  PRIMARY KEY (id),
  KEY idx_user_type (user_id, change_type),
  KEY idx_created (created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='积分流水表';

-- 10.8 withdrawal_records 提现记录表
DROP TABLE IF EXISTS `marketing`.`withdrawal_records`;
CREATE TABLE `marketing`.`withdrawal_records` (
  id BIGINT UNSIGNED NOT NULL COMMENT '雪花ID',
  user_id BIGINT UNSIGNED NOT NULL COMMENT '用户ID',
  amount DECIMAL(12,2) NOT NULL COMMENT '提现金额',
  fee DECIMAL(10,2) NOT NULL DEFAULT 0.00 COMMENT '手续费',
  actual_amount DECIMAL(12,2) NOT NULL COMMENT '实际到账',
  pay_method TINYINT UNSIGNED NOT NULL COMMENT '1支付宝 2微信 3银行卡',
  pay_account VARCHAR(255) NOT NULL COMMENT '收款账号（AES加密）',
  status TINYINT UNSIGNED NOT NULL DEFAULT 0 COMMENT '0待审核 1已打款 2已拒绝',
  audit_remark VARCHAR(256) DEFAULT NULL COMMENT '审核备注',
  audit_at DATETIME DEFAULT NULL COMMENT '审核时间',
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (id),
  KEY idx_user_status (user_id, status),
  KEY idx_status (status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='提现记录表';


-- ============================================
-- 营销域 V2 更新：二级分销 + 自定义邀请码 + 代理标签
-- ============================================





-- 4. agent_tags 代理标签表
DROP TABLE IF EXISTS `marketing`.`agent_tags`;
CREATE TABLE `marketing`.`agent_tags` (
  id BIGINT UNSIGNED NOT NULL COMMENT '雪花ID',
  user_id BIGINT UNSIGNED NOT NULL COMMENT '代理用户ID',
  agent_level TINYINT UNSIGNED NOT NULL DEFAULT 0 COMMENT '0普通 1银牌 2金牌 3钻石 4殿堂',
  agent_name VARCHAR(32) DEFAULT NULL COMMENT '代理自定义名称',
  badge_url VARCHAR(255) DEFAULT NULL COMMENT '徽章图片URL',
  discount_rate DECIMAL(3,2) NOT NULL DEFAULT 1.00 COMMENT '专属折扣率',
  customer_count INT UNSIGNED NOT NULL DEFAULT 0 COMMENT '名下客户数',
  total_sales DECIMAL(12,2) NOT NULL DEFAULT 0.00 COMMENT '累计销售额',
  status TINYINT UNSIGNED NOT NULL DEFAULT 0 COMMENT '0正常 1冻结',
  expired_at DATETIME DEFAULT NULL COMMENT '等级到期时间',
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (id),
  UNIQUE KEY uk_user (user_id),
  KEY idx_level (agent_level)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='代理标签表';

-- 5. agent_customers 代理客户关系表
DROP TABLE IF EXISTS `marketing`.`agent_customers`;
CREATE TABLE `marketing`.`agent_customers` (
  id BIGINT UNSIGNED NOT NULL COMMENT '雪花ID',
  agent_id BIGINT UNSIGNED NOT NULL COMMENT '代理用户ID',
  customer_id BIGINT UNSIGNED NOT NULL COMMENT '客户用户ID',
  source TINYINT UNSIGNED NOT NULL DEFAULT 1 COMMENT '1邀请注册 2手动绑定 3迁移',
  total_consume DECIMAL(12,2) NOT NULL DEFAULT 0.00 COMMENT '累计消费',
  last_pay_at DATETIME DEFAULT NULL COMMENT '最近消费时间',
  remark VARCHAR(128) DEFAULT NULL COMMENT '备注',
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (id),
  UNIQUE KEY uk_agent_customer (agent_id, customer_id),
  KEY idx_customer (customer_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='代理客户关系表';


-- ============================================
-- 营销域 V3 更新：创作者激励 + 任务体系 + 积分抽奖 + KOL赞助
-- 新增9张表
-- ============================================

-- 1. creators 创作者信息表
DROP TABLE IF EXISTS `marketing`.`creators`;
CREATE TABLE `marketing`.`creators` (
  id BIGINT UNSIGNED NOT NULL COMMENT '雪花ID',
  user_id BIGINT UNSIGNED NOT NULL COMMENT '用户ID',
  creator_level TINYINT UNSIGNED NOT NULL DEFAULT 0 COMMENT '0新手 1优质 2金牌 3殿堂',
  creator_name VARCHAR(32) DEFAULT NULL COMMENT '创作者名称',
  total_views BIGINT UNSIGNED NOT NULL DEFAULT 0 COMMENT '累计播放量',
  total_invites INT UNSIGNED NOT NULL DEFAULT 0 COMMENT '累计邀请人数',
  total_commission DECIMAL(12,2) NOT NULL DEFAULT 0.00 COMMENT '累计推广佣金',
  total_rewards DECIMAL(12,2) NOT NULL DEFAULT 0.00 COMMENT '累计视频奖励',
  video_count INT UNSIGNED NOT NULL DEFAULT 0 COMMENT '发布视频数',
  badge_url VARCHAR(255) DEFAULT NULL COMMENT '创作者徽章',
  status TINYINT UNSIGNED NOT NULL DEFAULT 0 COMMENT '0正常 1冻结',
  joined_at DATETIME DEFAULT NULL COMMENT '加入时间',
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uk_user (user_id),
  KEY idx_level (creator_level)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='创作者信息表';

-- 2. creator_videos 视频作品表
DROP TABLE IF EXISTS `marketing`.`creator_videos`;
CREATE TABLE `marketing`.`creator_videos` (
  id BIGINT UNSIGNED NOT NULL COMMENT '雪花ID',
  creator_id BIGINT UNSIGNED NOT NULL COMMENT '创作者ID',
  platform VARCHAR(16) NOT NULL COMMENT '平台',
  video_url VARCHAR(512) NOT NULL COMMENT '视频/文章链接',
  title VARCHAR(128) NOT NULL COMMENT '标题',
  views BIGINT UNSIGNED NOT NULL DEFAULT 0 COMMENT '播放量',
  likes INT UNSIGNED NOT NULL DEFAULT 0 COMMENT '点赞数',
  base_reward DECIMAL(10,2) NOT NULL DEFAULT 0.00 COMMENT '基础奖励',
  view_reward DECIMAL(10,2) NOT NULL DEFAULT 0.00 COMMENT '播放量奖励',
  level_multiplier DECIMAL(3,2) NOT NULL DEFAULT 1.00 COMMENT '等级倍率',
  total_reward DECIMAL(10,2) NOT NULL DEFAULT 0.00 COMMENT '总奖励',
  status TINYINT UNSIGNED NOT NULL DEFAULT 0 COMMENT '0待审核 1已通过 2已拒绝 3已结算',
  reject_reason VARCHAR(256) DEFAULT NULL COMMENT '拒绝原因',
  reviewed_at DATETIME DEFAULT NULL COMMENT '审核时间',
  settled_at DATETIME DEFAULT NULL COMMENT '结算时间',
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  KEY idx_creator (creator_id),
  KEY idx_platform (platform),
  KEY idx_status (status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='视频作品表';

-- 3. signin_records 签到记录表
DROP TABLE IF EXISTS `marketing`.`signin_records`;
CREATE TABLE `marketing`.`signin_records` (
  id BIGINT UNSIGNED NOT NULL COMMENT '雪花ID',
  user_id BIGINT UNSIGNED NOT NULL COMMENT '用户ID',
  signin_date DATE NOT NULL COMMENT '签到日期',
  points INT UNSIGNED NOT NULL DEFAULT 500 COMMENT '获得积分',
  continuous_days INT UNSIGNED NOT NULL DEFAULT 1 COMMENT '连续签到天数',
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uk_user_date (user_id, signin_date),
  KEY idx_date (signin_date)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='签到记录表';

-- 4. tasks 任务模板表
DROP TABLE IF EXISTS `marketing`.`tasks`;
CREATE TABLE `marketing`.`tasks` (
  id BIGINT UNSIGNED NOT NULL COMMENT '雪花ID',
  name VARCHAR(64) NOT NULL COMMENT '任务名称',
  type TINYINT UNSIGNED NOT NULL COMMENT '1新手 2每日 3成就',
  description VARCHAR(256) DEFAULT NULL COMMENT '任务描述',
  target INT UNSIGNED NOT NULL DEFAULT 1 COMMENT '完成目标',
  points_reward INT UNSIGNED NOT NULL DEFAULT 0 COMMENT '积分奖励',
  badge_reward VARCHAR(64) DEFAULT NULL COMMENT '徽章奖励',
  coupon_reward BIGINT UNSIGNED DEFAULT NULL COMMENT '优惠券奖励ID',
  sort INT UNSIGNED NOT NULL DEFAULT 0 COMMENT '排序',
  status TINYINT UNSIGNED NOT NULL DEFAULT 1 COMMENT '0禁用 1启用',
  start_at DATETIME DEFAULT NULL,
  end_at DATETIME DEFAULT NULL,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  KEY idx_type_status (type, status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='任务模板表';

-- 5. user_tasks 用户任务完成记录表
DROP TABLE IF EXISTS `marketing`.`user_tasks`;
CREATE TABLE `marketing`.`user_tasks` (
  id BIGINT UNSIGNED NOT NULL COMMENT '雪花ID',
  user_id BIGINT UNSIGNED NOT NULL COMMENT '用户ID',
  task_id BIGINT UNSIGNED NOT NULL COMMENT '任务ID',
  progress INT UNSIGNED NOT NULL DEFAULT 0 COMMENT '当前进度',
  status TINYINT UNSIGNED NOT NULL DEFAULT 0 COMMENT '0进行中 1已完成 2已领取',
  completed_at DATETIME DEFAULT NULL COMMENT '完成时间',
  claimed_at DATETIME DEFAULT NULL COMMENT '领取时间',
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uk_user_task (user_id, task_id),
  KEY idx_status (status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='用户任务表';

-- 6. achievements 成就表
DROP TABLE IF EXISTS `marketing`.`achievements`;
CREATE TABLE `marketing`.`achievements` (
  id BIGINT UNSIGNED NOT NULL COMMENT '雪花ID',
  name VARCHAR(64) NOT NULL COMMENT '成就名称',
  description VARCHAR(256) DEFAULT NULL COMMENT '成就描述',
  icon VARCHAR(256) DEFAULT NULL COMMENT '成就图标',
  condition_type VARCHAR(32) NOT NULL COMMENT '条件类型',
  condition_value INT UNSIGNED NOT NULL COMMENT '条件值',
  points_reward INT UNSIGNED NOT NULL DEFAULT 0 COMMENT '积分奖励',
  badge VARCHAR(64) DEFAULT NULL COMMENT '专属徽章',
  discount_reward DECIMAL(3,2) DEFAULT NULL COMMENT '折扣奖励',
  sort INT UNSIGNED NOT NULL DEFAULT 0,
  status TINYINT UNSIGNED NOT NULL DEFAULT 1,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  KEY idx_condition (condition_type)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='成就表';

-- 7. user_achievements 用户成就表
DROP TABLE IF EXISTS `marketing`.`user_achievements`;
CREATE TABLE `marketing`.`user_achievements` (
  id BIGINT UNSIGNED NOT NULL COMMENT '雪花ID',
  user_id BIGINT UNSIGNED NOT NULL COMMENT '用户ID',
  achievement_id BIGINT UNSIGNED NOT NULL COMMENT '成就ID',
  unlocked_at DATETIME NOT NULL COMMENT '解锁时间',
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uk_user_achievement (user_id, achievement_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='用户成就表';

-- 8. point_lottery_records 积分抽奖记录表
DROP TABLE IF EXISTS `marketing`.`point_lottery_records`;
CREATE TABLE `marketing`.`point_lottery_records` (
  id BIGINT UNSIGNED NOT NULL COMMENT '雪花ID',
  user_id BIGINT UNSIGNED NOT NULL COMMENT '用户ID',
  points_cost INT UNSIGNED NOT NULL DEFAULT 2000 COMMENT '消耗积分',
  prize_type TINYINT UNSIGNED NOT NULL COMMENT '1积分 2优惠券 3实物 4谢谢参与',
  prize_name VARCHAR(64) DEFAULT NULL COMMENT '奖品名称',
  prize_value DECIMAL(10,2) DEFAULT NULL COMMENT '奖品价值',
  status TINYINT UNSIGNED NOT NULL DEFAULT 0 COMMENT '0已发放 1待发货 2已发货',
  lottery_time DATETIME NOT NULL COMMENT '抽奖时间',
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  KEY idx_user (user_id),
  KEY idx_prize_type (prize_type)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='积分抽奖记录表';

-- 9. kol_sponsorships KOL赞助申请表
DROP TABLE IF EXISTS `marketing`.`kol_sponsorships`;
CREATE TABLE `marketing`.`kol_sponsorships` (
  id BIGINT UNSIGNED NOT NULL COMMENT '雪花ID',
  user_id BIGINT UNSIGNED NOT NULL COMMENT '用户ID',
  sponsor_type VARCHAR(32) NOT NULL COMMENT '类型',
  platform_url VARCHAR(512) DEFAULT NULL COMMENT '平台链接',
  follower_count INT UNSIGNED DEFAULT NULL COMMENT '粉丝数',
  content_plan TEXT DEFAULT NULL COMMENT '内容计划',
  server_config VARCHAR(128) DEFAULT NULL COMMENT '申请服务器配置',
  status TINYINT UNSIGNED NOT NULL DEFAULT 0 COMMENT '0待审核 1已通过 2已拒绝 3已结束',
  audit_remark VARCHAR(256) DEFAULT NULL COMMENT '审核备注',
  audit_at DATETIME DEFAULT NULL COMMENT '审核时间',
  expired_at DATETIME DEFAULT NULL COMMENT '赞助到期时间',
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  KEY idx_user (user_id),
  KEY idx_status (status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='KOL赞助申请表';


-- 10. marketing_configs 营销全局配置表
DROP TABLE IF EXISTS `marketing`.`marketing_configs`;
CREATE TABLE `marketing`.`marketing_configs` (
  id BIGINT UNSIGNED NOT NULL COMMENT '雪花ID',
  config_key VARCHAR(128) NOT NULL COMMENT '配置键',
  config_value TEXT NOT NULL COMMENT '配置值',
  config_type VARCHAR(16) NOT NULL DEFAULT 'string' COMMENT 'switch/number/string/json/select',
  category VARCHAR(32) NOT NULL COMMENT '分类',
  title VARCHAR(64) NOT NULL COMMENT '配置名称',
  description VARCHAR(256) DEFAULT NULL COMMENT '配置说明',
  options TEXT DEFAULT NULL COMMENT '下拉选项JSON',
  min_value DECIMAL(10,4) DEFAULT NULL COMMENT '最小值',
  max_value DECIMAL(10,4) DEFAULT NULL COMMENT '最大值',
  sort INT UNSIGNED NOT NULL DEFAULT 0 COMMENT '排序',
  status TINYINT UNSIGNED NOT NULL DEFAULT 1 COMMENT '0禁用 1启用',
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uk_config_key (config_key),
  KEY idx_category (category),
  KEY idx_status (status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='营销全局配置表';

-- 11. channel_bindings 渠道用户绑定表
DROP TABLE IF EXISTS `marketing`.`channel_bindings`;
CREATE TABLE `marketing`.`channel_bindings` (
  id BIGINT UNSIGNED NOT NULL COMMENT '雪花ID',
  user_id BIGINT UNSIGNED NOT NULL COMMENT '用户ID',
  channel_type VARCHAR(32) NOT NULL COMMENT '渠道类型',
  open_id VARCHAR(128) NOT NULL COMMENT '渠道OpenID',
  union_id VARCHAR(128) DEFAULT NULL COMMENT '微信UnionID',
  nickname VARCHAR(64) DEFAULT NULL COMMENT '渠道昵称',
  avatar_url VARCHAR(512) DEFAULT NULL COMMENT '渠道头像',
  extra_info JSON DEFAULT NULL COMMENT '渠道额外信息',
  bound_at DATETIME NOT NULL COMMENT '绑定时间',
  unbound_at DATETIME DEFAULT NULL COMMENT '解绑时间',
  status TINYINT UNSIGNED NOT NULL DEFAULT 1 COMMENT '0已解绑 1已绑定',
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uk_channel_openid (channel_type, open_id),
  KEY idx_user (user_id),
  KEY idx_unionid (union_id),
  KEY idx_status (status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='渠道用户绑定表';

-- ============================================
-- 支付域新增表
-- ============================================

DROP TABLE IF EXISTS `payment`.`payment_channels`;
CREATE TABLE `payment`.`payment_channels` (
  id BIGINT UNSIGNED NOT NULL COMMENT '雪花ID',
  channel_code VARCHAR(32) NOT NULL COMMENT '渠道编码',
  channel_name VARCHAR(64) NOT NULL COMMENT '渠道名称',
  channel_type VARCHAR(16) NOT NULL COMMENT 'pc/mobile/qrcode/h5/app',
  app_id VARCHAR(128) DEFAULT NULL,
  app_secret VARCHAR(256) DEFAULT NULL,
  merchant_id VARCHAR(128) DEFAULT NULL,
  private_key TEXT DEFAULT NULL,
  public_key TEXT DEFAULT NULL,
  cert_path VARCHAR(256) DEFAULT NULL,
  notify_url VARCHAR(256) DEFAULT NULL,
  return_url VARCHAR(256) DEFAULT NULL,
  gateway_url VARCHAR(256) DEFAULT NULL,
  fee_rate DECIMAL(5,4) NOT NULL DEFAULT 0.0060,
  min_amount DECIMAL(10,2) NOT NULL DEFAULT 0.01,
  max_amount DECIMAL(10,2) NOT NULL DEFAULT 100000.00,
  sort INT UNSIGNED NOT NULL DEFAULT 0,
  status TINYINT UNSIGNED NOT NULL DEFAULT 0,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uk_channel_code_type (channel_code, channel_type),
  KEY idx_status (status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='支付渠道配置表';

DROP TABLE IF EXISTS `payment`.`payment_orders`;
CREATE TABLE `payment`.`payment_orders` (
  id BIGINT UNSIGNED NOT NULL COMMENT '雪花ID',
  payment_no VARCHAR(32) NOT NULL COMMENT '支付单号',
  order_id BIGINT UNSIGNED NOT NULL,
  order_type VARCHAR(32) NOT NULL,
  user_id BIGINT UNSIGNED NOT NULL,
  amount DECIMAL(12,2) NOT NULL,
  actual_amount DECIMAL(12,2) DEFAULT NULL,
  fee_amount DECIMAL(12,2) DEFAULT NULL,
  channel_id BIGINT UNSIGNED DEFAULT NULL,
  channel_code VARCHAR(32) DEFAULT NULL,
  third_payment_no VARCHAR(64) DEFAULT NULL,
  status TINYINT UNSIGNED NOT NULL DEFAULT 0,
  paid_at DATETIME DEFAULT NULL,
  expired_at DATETIME DEFAULT NULL,
  client_ip VARCHAR(64) DEFAULT NULL,
  user_agent VARCHAR(512) DEFAULT NULL,
  remark VARCHAR(256) DEFAULT NULL,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uk_payment_no (payment_no),
  KEY idx_order (order_id, order_type),
  KEY idx_user (user_id),
  KEY idx_status (status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='支付订单表';

DROP TABLE IF EXISTS `payment`.`payment_refunds`;
CREATE TABLE `payment`.`payment_refunds` (
  id BIGINT UNSIGNED NOT NULL COMMENT '雪花ID',
  refund_no VARCHAR(32) NOT NULL COMMENT '退款单号',
  payment_id BIGINT UNSIGNED NOT NULL,
  order_id BIGINT UNSIGNED NOT NULL,
  user_id BIGINT UNSIGNED NOT NULL,
  refund_amount DECIMAL(12,2) NOT NULL,
  reason VARCHAR(256) DEFAULT NULL,
  third_refund_no VARCHAR(64) DEFAULT NULL,
  status TINYINT UNSIGNED NOT NULL DEFAULT 0,
  audit_by BIGINT UNSIGNED DEFAULT NULL,
  audit_at DATETIME DEFAULT NULL,
  audit_remark VARCHAR(256) DEFAULT NULL,
  refunded_at DATETIME DEFAULT NULL,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uk_refund_no (refund_no),
  KEY idx_payment (payment_id),
  KEY idx_user (user_id),
  KEY idx_status (status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='退款记录表';

DROP TABLE IF EXISTS `payment`.`payment_transactions`;
CREATE TABLE `payment`.`payment_transactions` (
  id BIGINT UNSIGNED NOT NULL COMMENT '雪花ID',
  payment_id BIGINT UNSIGNED NOT NULL,
  transaction_type VARCHAR(32) NOT NULL,
  channel_code VARCHAR(32) NOT NULL,
  request_data JSON DEFAULT NULL,
  response_data JSON DEFAULT NULL,
  third_trade_no VARCHAR(64) DEFAULT NULL,
  amount DECIMAL(12,2) DEFAULT NULL,
  status VARCHAR(16) NOT NULL,
  error_code VARCHAR(64) DEFAULT NULL,
  error_msg VARCHAR(256) DEFAULT NULL,
  client_ip VARCHAR(64) DEFAULT NULL,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  KEY idx_payment (payment_id),
  KEY idx_channel (channel_code),
  KEY idx_type (transaction_type)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='支付流水表';

-- ============================================
-- 系统域新增表（消息模块）
-- ============================================

DROP TABLE IF EXISTS `system`.`message_channels`;
CREATE TABLE `system`.`message_channels` (
  id BIGINT UNSIGNED NOT NULL COMMENT '雪花ID',
  channel_code VARCHAR(32) NOT NULL,
  channel_name VARCHAR(64) NOT NULL,
  channel_type VARCHAR(16) NOT NULL COMMENT 'sms/email',
  app_key VARCHAR(128) DEFAULT NULL,
  app_secret VARCHAR(256) DEFAULT NULL,
  endpoint VARCHAR(256) DEFAULT NULL,
  sign_name VARCHAR(64) DEFAULT NULL,
  from_email VARCHAR(128) DEFAULT NULL,
  from_name VARCHAR(64) DEFAULT NULL,
  smtp_host VARCHAR(128) DEFAULT NULL,
  smtp_port INT UNSIGNED DEFAULT NULL,
  smtp_secure TINYINT UNSIGNED DEFAULT 0,
  daily_limit INT UNSIGNED NOT NULL DEFAULT 10000,
  rate_limit INT UNSIGNED NOT NULL DEFAULT 100,
  status TINYINT UNSIGNED NOT NULL DEFAULT 0,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uk_channel_code (channel_code),
  KEY idx_type (channel_type),
  KEY idx_status (status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='消息渠道配置表';

DROP TABLE IF EXISTS `system`.`message_templates`;
CREATE TABLE `system`.`message_templates` (
  id BIGINT UNSIGNED NOT NULL COMMENT '雪花ID',
  template_code VARCHAR(64) NOT NULL,
  template_name VARCHAR(64) NOT NULL,
  channel_type VARCHAR(16) NOT NULL,
  channel_id BIGINT UNSIGNED DEFAULT NULL,
  third_template_id VARCHAR(128) DEFAULT NULL,
  subject VARCHAR(256) DEFAULT NULL,
  content TEXT NOT NULL,
  variables JSON DEFAULT NULL,
  is_html TINYINT UNSIGNED NOT NULL DEFAULT 0,
  status TINYINT UNSIGNED NOT NULL DEFAULT 1,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uk_template_code (template_code),
  KEY idx_channel_type (channel_type),
  KEY idx_status (status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='消息模板表';

DROP TABLE IF EXISTS `system`.`message_records`;
CREATE TABLE `system`.`message_records` (
  id BIGINT UNSIGNED NOT NULL COMMENT '雪花ID',
  user_id BIGINT UNSIGNED DEFAULT NULL,
  channel_type VARCHAR(16) NOT NULL,
  channel_id BIGINT UNSIGNED NOT NULL,
  template_id BIGINT UNSIGNED DEFAULT NULL,
  receiver VARCHAR(128) NOT NULL,
  subject VARCHAR(256) DEFAULT NULL,
  content TEXT NOT NULL,
  variables JSON DEFAULT NULL,
  third_message_id VARCHAR(128) DEFAULT NULL,
  status TINYINT UNSIGNED NOT NULL DEFAULT 0,
  error_code VARCHAR(64) DEFAULT NULL,
  error_msg VARCHAR(512) DEFAULT NULL,
  retry_count INT UNSIGNED NOT NULL DEFAULT 0,
  sent_at DATETIME DEFAULT NULL,
  delivered_at DATETIME DEFAULT NULL,
  client_ip VARCHAR(64) DEFAULT NULL,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  KEY idx_user (user_id),
  KEY idx_receiver (receiver),
  KEY idx_channel (channel_type, channel_id),
  KEY idx_status (status),
  KEY idx_created (created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='消息发送记录表';

DROP TABLE IF EXISTS `system`.`message_verification_codes`;
CREATE TABLE `system`.`message_verification_codes` (
  id BIGINT UNSIGNED NOT NULL COMMENT '雪花ID',
  receiver VARCHAR(128) NOT NULL,
  channel_type VARCHAR(16) NOT NULL,
  scene VARCHAR(32) NOT NULL,
  code VARCHAR(8) NOT NULL,
  code_hash VARCHAR(128) NOT NULL,
  ip VARCHAR(64) DEFAULT NULL,
  user_agent VARCHAR(512) DEFAULT NULL,
  expire_at DATETIME NOT NULL,
  used_at DATETIME DEFAULT NULL,
  status TINYINT UNSIGNED NOT NULL DEFAULT 0,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  KEY idx_receiver_scene (receiver, scene),
  KEY idx_ip (ip),
  KEY idx_expire (expire_at),
  KEY idx_status (status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='验证码记录表';

-- ============================================
-- 销售客服域新增表
-- ============================================

DROP TABLE IF EXISTS `system`.`sales_staff`;
CREATE TABLE `system`.`sales_staff` (
  id BIGINT UNSIGNED NOT NULL COMMENT '雪花ID',
  admin_id BIGINT UNSIGNED NOT NULL,
  staff_no VARCHAR(32) NOT NULL,
  name VARCHAR(32) NOT NULL,
  nickname VARCHAR(32) DEFAULT NULL,
  avatar VARCHAR(256) DEFAULT NULL,
  phone VARCHAR(20) DEFAULT NULL,
  wechat VARCHAR(64) DEFAULT NULL,
  qq VARCHAR(20) DEFAULT NULL,
  email VARCHAR(128) DEFAULT NULL,
  role VARCHAR(32) NOT NULL DEFAULT 'sales',
  level TINYINT UNSIGNED NOT NULL DEFAULT 1,
  permission_config_id BIGINT UNSIGNED DEFAULT NULL,
  customer_limit INT UNSIGNED NOT NULL DEFAULT 500,
  customer_count INT UNSIGNED NOT NULL DEFAULT 0,
  total_sales DECIMAL(14,2) NOT NULL DEFAULT 0.00,
  total_commission DECIMAL(12,2) NOT NULL DEFAULT 0.00,
  status TINYINT UNSIGNED NOT NULL DEFAULT 1,
  join_date DATE DEFAULT NULL,
  leave_date DATE DEFAULT NULL,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uk_admin (admin_id),
  UNIQUE KEY uk_staff_no (staff_no),
  KEY idx_role (role),
  KEY idx_status (status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='销售客服人员表';

DROP TABLE IF EXISTS `system`.`sales_customers`;
CREATE TABLE `system`.`sales_customers` (
  id BIGINT UNSIGNED NOT NULL COMMENT '雪花ID',
  sales_id BIGINT UNSIGNED NOT NULL,
  customer_id BIGINT UNSIGNED NOT NULL,
  source VARCHAR(32) NOT NULL DEFAULT 'register',
  register_sales_code VARCHAR(32) DEFAULT NULL,
  assigned_at DATETIME NOT NULL,
  last_contact_at DATETIME DEFAULT NULL,
  followup_count INT UNSIGNED NOT NULL DEFAULT 0,
  customer_level TINYINT UNSIGNED NOT NULL DEFAULT 0,
  remark VARCHAR(512) DEFAULT NULL,
  tags JSON DEFAULT NULL,
  status TINYINT UNSIGNED NOT NULL DEFAULT 1,
  transferred_to BIGINT UNSIGNED DEFAULT NULL,
  transferred_at DATETIME DEFAULT NULL,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uk_sales_customer (sales_id, customer_id),
  KEY idx_customer (customer_id),
  KEY idx_source (source),
  KEY idx_level (customer_level),
  KEY idx_status (status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='销售客户关系表';

DROP TABLE IF EXISTS `system`.`sales_permissions`;
CREATE TABLE `system`.`sales_permissions` (
  id BIGINT UNSIGNED NOT NULL COMMENT '雪花ID',
  name VARCHAR(64) NOT NULL,
  role VARCHAR(32) NOT NULL DEFAULT 'sales',
  field_permissions JSON NOT NULL,
  can_view_balance TINYINT UNSIGNED NOT NULL DEFAULT 0,
  can_view_consumption TINYINT UNSIGNED NOT NULL DEFAULT 0,
  can_view_realname TINYINT UNSIGNED NOT NULL DEFAULT 0,
  can_view_idcard TINYINT UNSIGNED NOT NULL DEFAULT 0,
  can_view_phone TINYINT UNSIGNED NOT NULL DEFAULT 1,
  can_view_email TINYINT UNSIGNED NOT NULL DEFAULT 1,
  can_view_password TINYINT UNSIGNED NOT NULL DEFAULT 0,
  can_export_customer TINYINT UNSIGNED NOT NULL DEFAULT 0,
  can_transfer_customer TINYINT UNSIGNED NOT NULL DEFAULT 0,
  can_assign_customer TINYINT UNSIGNED NOT NULL DEFAULT 0,
  can_view_all_customers TINYINT UNSIGNED NOT NULL DEFAULT 0,
  can_view_team_performance TINYINT UNSIGNED NOT NULL DEFAULT 0,
  data_scope VARCHAR(16) NOT NULL DEFAULT 'self',
  status TINYINT UNSIGNED NOT NULL DEFAULT 1,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  KEY idx_role (role),
  KEY idx_status (status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='销售数据权限配置表';

DROP TABLE IF EXISTS `system`.`sales_followups`;
CREATE TABLE `system`.`sales_followups` (
  id BIGINT UNSIGNED NOT NULL COMMENT '雪花ID',
  sales_id BIGINT UNSIGNED NOT NULL,
  customer_id BIGINT UNSIGNED NOT NULL,
  followup_type VARCHAR(32) NOT NULL,
  content TEXT NOT NULL,
  next_followup_at DATETIME DEFAULT NULL,
  customer_level_before TINYINT UNSIGNED DEFAULT NULL,
  customer_level_after TINYINT UNSIGNED DEFAULT NULL,
  attachments JSON DEFAULT NULL,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  KEY idx_sales (sales_id),
  KEY idx_customer (customer_id),
  KEY idx_followup_type (followup_type),
  KEY idx_next_followup (next_followup_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='销售跟进记录表';

DROP TABLE IF EXISTS `system`.`sales_performance`;
CREATE TABLE `system`.`sales_performance` (
  id BIGINT UNSIGNED NOT NULL COMMENT '雪花ID',
  sales_id BIGINT UNSIGNED NOT NULL,
  period_type VARCHAR(16) NOT NULL,
  period_start DATE NOT NULL,
  period_end DATE NOT NULL,
  new_customer_count INT UNSIGNED NOT NULL DEFAULT 0,
  active_customer_count INT UNSIGNED NOT NULL DEFAULT 0,
  order_count INT UNSIGNED NOT NULL DEFAULT 0,
  sales_amount DECIMAL(14,2) NOT NULL DEFAULT 0.00,
  renewal_amount DECIMAL(14,2) NOT NULL DEFAULT 0.00,
  upgrade_amount DECIMAL(14,2) NOT NULL DEFAULT 0.00,
  commission_amount DECIMAL(12,2) NOT NULL DEFAULT 0.00,
  commission_rate DECIMAL(5,4) NOT NULL DEFAULT 0.0500,
  followup_count INT UNSIGNED NOT NULL DEFAULT 0,
  conversion_rate DECIMAL(5,4) DEFAULT NULL,
  `rank` INT UNSIGNED DEFAULT NULL,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uk_sales_period (sales_id, period_type, period_start),
  KEY idx_period (period_type, period_start)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='销售业绩表';

-- ============================================
-- 域: 上下游对接 - P0 核心表（复用既有 10 个 Schema）
-- ============================================

CREATE TABLE IF NOT EXISTS `common`.`suppliers` (
  `id` BIGINT UNSIGNED NOT NULL COMMENT '雪花ID',
  `name` VARCHAR(100) NOT NULL, `type` VARCHAR(20) NOT NULL DEFAULT 'default',
  `url` VARCHAR(255) NOT NULL, `username` VARCHAR(100) NOT NULL,
  `token` TEXT NOT NULL COMMENT 'AES-GCM加密API密钥', `secret` TEXT NULL COMMENT 'AES-GCM加密私钥',
  `rate` DECIMAL(10,4) NOT NULL DEFAULT 1.0000, `currency_code` VARCHAR(10) NULL,
  `status` TINYINT NOT NULL DEFAULT 1, `remark` VARCHAR(500) NULL,
  `created_at` BIGINT NOT NULL, `updated_at` BIGINT NOT NULL,
  PRIMARY KEY (`id`), KEY `idx_type` (`type`), KEY `idx_status` (`status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='上游供应商';

CREATE TABLE IF NOT EXISTS `product`.`upstream_products` (
  `id` BIGINT UNSIGNED NOT NULL COMMENT '雪花ID', `supplier_id` BIGINT UNSIGNED NOT NULL,
  `upstream_product_id` INT NOT NULL, `product_id` BIGINT UNSIGNED NULL,
  `name` VARCHAR(200) NOT NULL, `description` TEXT NULL, `product_group_id` BIGINT UNSIGNED NOT NULL,
  `profit_type` TINYINT NOT NULL DEFAULT 0, `profit_value` DECIMAL(10,2) NOT NULL DEFAULT 0,
  `renew_profit_type` TINYINT NOT NULL DEFAULT 0, `renew_profit_value` DECIMAL(10,2) NOT NULL DEFAULT 0,
  `upgrade_profit_type` TINYINT NOT NULL DEFAULT 0, `upgrade_profit_value` DECIMAL(10,2) NOT NULL DEFAULT 0,
  `auto_setup` TINYINT NOT NULL DEFAULT 1, `certification` TINYINT NOT NULL DEFAULT 0, `hidden` TINYINT NOT NULL DEFAULT 0,
  `mode` VARCHAR(20) NOT NULL DEFAULT 'only_api', `price_basis` VARCHAR(20) NOT NULL DEFAULT 'agent',
  `sync` TINYINT NOT NULL DEFAULT 0, `need_manual_sync` TINYINT NOT NULL DEFAULT 0,
  `upstream_price` DECIMAL(12,2) NULL, `price` DECIMAL(12,2) NULL, `cycle` VARCHAR(50) NULL, `pay_type` VARCHAR(50) NULL,
  `created_at` BIGINT NOT NULL, `updated_at` BIGINT NOT NULL,
  PRIMARY KEY (`id`), UNIQUE KEY `uk_supplier_upstream_product` (`supplier_id`,`upstream_product_id`), KEY `idx_supplier` (`supplier_id`), KEY `idx_product` (`product_id`), KEY `idx_mode` (`mode`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='上游代理产品';

CREATE TABLE IF NOT EXISTS `product`.`upstream_catalog` (
  `id` BIGINT UNSIGNED NOT NULL, `supplier_id` BIGINT UNSIGNED NOT NULL, `upstream_product_id` INT NOT NULL,
  `name` VARCHAR(200) NOT NULL, `description` TEXT NULL, `type` VARCHAR(64) NULL, `group_id` INT NULL,
  `pay_type` VARCHAR(64) NULL, `price` DECIMAL(12,2) DEFAULT 0, `cycle` VARCHAR(50) NULL, `currency_code` VARCHAR(16) NULL,
  `status` VARCHAR(16) NOT NULL DEFAULT 'active', `synced_at` BIGINT NOT NULL, `created_at` BIGINT NOT NULL, `updated_at` BIGINT NOT NULL,
  PRIMARY KEY (`id`), UNIQUE KEY `uk_supplier_product` (`supplier_id`,`upstream_product_id`), KEY `idx_supplier_status` (`supplier_id`,`status`), KEY `idx_name` (`name`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='上游商品目录快照';

CREATE TABLE IF NOT EXISTS `system`.`sync_tasks` (
  `id` BIGINT UNSIGNED NOT NULL, `supplier_id` BIGINT UNSIGNED NOT NULL, `type` VARCHAR(32) NOT NULL, `status` VARCHAR(16) NOT NULL,
  `started_at` BIGINT DEFAULT 0, `finished_at` BIGINT DEFAULT 0, `error` TEXT NULL, `trigger_by` VARCHAR(16) NOT NULL,
  `total` INT DEFAULT 0, `progress` INT DEFAULT 0, PRIMARY KEY (`id`), KEY `idx_supplier_status` (`supplier_id`,`status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='上游同步任务';

CREATE TABLE IF NOT EXISTS `order`.`upstream_orders` (
  `id` BIGINT UNSIGNED NOT NULL COMMENT '雪花ID', `order_id` BIGINT UNSIGNED NOT NULL,
  `supplier_id` BIGINT UNSIGNED NOT NULL, `upstream_product_id` INT NOT NULL, `upstream_order_id` VARCHAR(100) NOT NULL,
  `upstream_host_id` VARCHAR(100) NULL, `status` VARCHAR(32) NOT NULL, `cost` DECIMAL(12,2) NOT NULL DEFAULT 0,
  `balance_before` DECIMAL(12,2) NOT NULL DEFAULT 0, `balance_after` DECIMAL(12,2) NOT NULL DEFAULT 0,
  `attempt_count` TINYINT UNSIGNED NOT NULL DEFAULT 0, `last_error` VARCHAR(512) NULL,
  `created_at` BIGINT NOT NULL, `updated_at` BIGINT NOT NULL,
  PRIMARY KEY (`id`), UNIQUE KEY `uk_order` (`order_id`), KEY `idx_supplier_status` (`supplier_id`,`status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='本地订单与上游订单关联';

CREATE TABLE IF NOT EXISTS `instance`.`upstream_hosts` (
  `id` BIGINT UNSIGNED NOT NULL COMMENT '雪花ID', `host_id` BIGINT UNSIGNED NOT NULL,
  `supplier_id` BIGINT UNSIGNED NOT NULL, `upstream_host_id` VARCHAR(100) NULL, `status` VARCHAR(32) NOT NULL,
  `due_time` BIGINT NULL, `created_at` BIGINT NOT NULL, `updated_at` BIGINT NOT NULL,
  PRIMARY KEY (`id`), UNIQUE KEY `uk_host` (`host_id`), KEY `idx_supplier` (`supplier_id`), KEY `idx_status` (`status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='本地产品实例与上游实例关联';

-- ============================================
-- 营销套件 M1-M6（营销域 + 销售域，金额统一 bigint 分）
-- M1 邀请码 / M2 多级分销 / M4 优惠券流水 / M5 销售客服
-- M6 配置全部存 system.system_configs（marketing.* 前缀键）
-- ============================================

-- M1.1 invites 邀请码表（每用户一个生效码）
DROP TABLE IF EXISTS `marketing`.`invites`;
CREATE TABLE `marketing`.`invites` (
  id BIGINT UNSIGNED NOT NULL COMMENT '雪花ID',
  user_id BIGINT UNSIGNED NOT NULL COMMENT '所属用户ID',
  code VARCHAR(16) NOT NULL COMMENT '邀请码（唯一，生效码）',
  status TINYINT UNSIGNED NOT NULL DEFAULT 1 COMMENT '0待审核 1启用 2禁用 3已驳回',
  reject_reason VARCHAR(255) DEFAULT NULL COMMENT '驳回原因',
  audited_at DATETIME DEFAULT NULL COMMENT '审核时间',
  invite_count INT UNSIGNED NOT NULL DEFAULT 0 COMMENT '一级邀请人数（冗余）',
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (id),
  UNIQUE KEY uk_code (code),
  UNIQUE KEY uk_user (user_id),
  KEY idx_status (status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='邀请码表';

-- M1.2 invite_relations 邀请关系树（注册成功建立，level 按 1..3 链路推导）
DROP TABLE IF EXISTS `marketing`.`invite_relations`;
CREATE TABLE `marketing`.`invite_relations` (
  id BIGINT UNSIGNED NOT NULL COMMENT '雪花ID',
  inviter_id BIGINT UNSIGNED NOT NULL COMMENT '邀请人用户ID',
  invitee_id BIGINT UNSIGNED NOT NULL COMMENT '被邀请人用户ID',
  level TINYINT UNSIGNED NOT NULL DEFAULT 1 COMMENT '关系层级：1一级 2二级 3三级',
  invite_code VARCHAR(16) NOT NULL COMMENT '注册时使用的邀请码',
  status TINYINT UNSIGNED NOT NULL DEFAULT 1 COMMENT '1正常 2无效',
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (id),
  UNIQUE KEY uk_invitee_level (invitee_id, level),
  KEY idx_inviter_level (inviter_id, level),
  KEY idx_status (status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='邀请关系表';

-- M2.1 rewards 分销奖励表（支付成功按配置生成，唯一键幂等）
DROP TABLE IF EXISTS `marketing`.`rewards`;
CREATE TABLE `marketing`.`rewards` (
  id BIGINT UNSIGNED NOT NULL COMMENT '雪花ID',
  order_id BIGINT UNSIGNED NOT NULL COMMENT '关联订单/充值单ID',
  order_no VARCHAR(32) NOT NULL COMMENT '关联单号',
  biz_type TINYINT UNSIGNED NOT NULL DEFAULT 1 COMMENT '来源类型：1订单 2充值',
  user_id BIGINT UNSIGNED NOT NULL COMMENT '消费者（被推荐人）ID',
  reward_user_id BIGINT UNSIGNED NOT NULL COMMENT '获奖人（推荐人）ID',
  level TINYINT UNSIGNED NOT NULL DEFAULT 1 COMMENT '奖励层级：1..3',
  reward_type TINYINT UNSIGNED NOT NULL DEFAULT 1 COMMENT '1百分比 2固定金额',
  base_amount BIGINT NOT NULL DEFAULT 0 COMMENT '计奖基数（实付金额，分）',
  amount BIGINT NOT NULL DEFAULT 0 COMMENT '奖励金额（分）',
  status TINYINT UNSIGNED NOT NULL DEFAULT 0 COMMENT '0待结算 1已结算 2已冻结 3已回收',
  settled_at DATETIME DEFAULT NULL COMMENT '结算时间',
  remark VARCHAR(255) DEFAULT NULL COMMENT '备注（回收/冻结原因）',
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (id),
  UNIQUE KEY uk_order_reward (order_id, reward_user_id, level),
  KEY idx_reward_user (reward_user_id, status),
  KEY idx_user (user_id),
  KEY idx_order (order_id),
  KEY idx_status (status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='分销奖励表';

-- M4.1 coupon_logs 优惠券流水表
DROP TABLE IF EXISTS `marketing`.`coupon_logs`;
CREATE TABLE `marketing`.`coupon_logs` (
  id BIGINT UNSIGNED NOT NULL COMMENT '雪花ID',
  coupon_id BIGINT UNSIGNED NOT NULL COMMENT '优惠券模板ID',
  user_coupon_id BIGINT UNSIGNED DEFAULT NULL COMMENT '用户券ID',
  user_id BIGINT UNSIGNED DEFAULT NULL COMMENT '用户ID',
  action TINYINT UNSIGNED NOT NULL COMMENT '1创建 2发放 3领取 4核销 5停用 6作废 7过期',
  order_id BIGINT UNSIGNED DEFAULT NULL COMMENT '核销订单ID',
  operator VARCHAR(32) DEFAULT NULL COMMENT '操作人（admin/用户名/系统）',
  remark VARCHAR(255) DEFAULT NULL COMMENT '备注',
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  PRIMARY KEY (id),
  KEY idx_coupon (coupon_id, created_at),
  KEY idx_user (user_id, created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='优惠券流水表';

-- M5.1 sales_users 销售客服账号表（独立登录，注册页按工号绑定）
DROP TABLE IF EXISTS `system`.`sales_users`;
CREATE TABLE `system`.`sales_users` (
  id BIGINT UNSIGNED NOT NULL COMMENT '雪花ID',
  staff_no VARCHAR(32) NOT NULL COMMENT '销售工号（注册页填写，唯一）',
  username VARCHAR(64) NOT NULL COMMENT '销售后台登录名（唯一）',
  password_hash VARCHAR(255) NOT NULL COMMENT 'bcrypt 哈希',
  name VARCHAR(64) NOT NULL COMMENT '姓名/称呼',
  phone VARCHAR(20) DEFAULT NULL,
  email VARCHAR(128) DEFAULT NULL,
  user_id BIGINT UNSIGNED NOT NULL DEFAULT 0 COMMENT '可选关联客户账号（预留共享登录）',
  status TINYINT UNSIGNED NOT NULL DEFAULT 1 COMMENT '1启用 2禁用',
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (id),
  UNIQUE KEY uk_staff_no (staff_no),
  UNIQUE KEY uk_username (username),
  KEY idx_status (status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='销售客服账号表';

-- M5.2 sales_visibility_rules 销售客户字段可见性规则（sales_id=0 全局默认）
DROP TABLE IF EXISTS `system`.`sales_visibility_rules`;
CREATE TABLE `system`.`sales_visibility_rules` (
  id BIGINT UNSIGNED NOT NULL COMMENT '雪花ID',
  sales_id BIGINT UNSIGNED NOT NULL COMMENT '销售ID（0=全局默认规则）',
  can_view_mobile TINYINT UNSIGNED NOT NULL DEFAULT 1 COMMENT '可见手机号',
  can_view_email TINYINT UNSIGNED NOT NULL DEFAULT 1 COMMENT '可见邮箱',
  can_view_orders TINYINT UNSIGNED NOT NULL DEFAULT 1 COMMENT '可见订单',
  can_view_spend TINYINT UNSIGNED NOT NULL DEFAULT 0 COMMENT '可见消费额',
  can_view_balance TINYINT UNSIGNED NOT NULL DEFAULT 0 COMMENT '可见余额',
  can_view_realname TINYINT UNSIGNED NOT NULL DEFAULT 0 COMMENT '可见实名信息',
  can_followup TINYINT UNSIGNED NOT NULL DEFAULT 1 COMMENT '可写跟进记录',
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  PRIMARY KEY (id),
  UNIQUE KEY uk_sales (sales_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='销售客户字段可见性规则';
