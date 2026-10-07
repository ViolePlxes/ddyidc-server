-- Prepaid promoter coupons. Financial records are append-only and survive disable.
CREATE TABLE IF NOT EXISTS marketing.promo_coupon_settings (
 id INT PRIMARY KEY, enabled BOOLEAN NOT NULL DEFAULT FALSE,
 direct_enabled BOOLEAN NOT NULL DEFAULT TRUE, offer_enabled BOOLEAN NOT NULL DEFAULT TRUE,
 points_enabled BOOLEAN NOT NULL DEFAULT TRUE, balance_enabled BOOLEAN NOT NULL DEFAULT TRUE,
 renewal_enabled BOOLEAN NOT NULL DEFAULT FALSE, review_required BOOLEAN NOT NULL DEFAULT TRUE,
 points_per_yuan BIGINT NOT NULL DEFAULT 1000, face_max DECIMAL(14,2) NOT NULL DEFAULT 20.00,
 batch_max INT NOT NULL DEFAULT 100, daily_count INT NOT NULL DEFAULT 100,
 daily_budget DECIMAL(14,2) NOT NULL DEFAULT 200.00, valid_days_max INT NOT NULL DEFAULT 30,
 product_ids JSON NOT NULL, maintenance_seconds INT NOT NULL DEFAULT 60, version BIGINT NOT NULL DEFAULT 1,
 updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6)
);
INSERT IGNORE INTO marketing.promo_coupon_settings (id,product_ids) VALUES (1,JSON_ARRAY());
CREATE TABLE IF NOT EXISTS marketing.promo_coupon_templates (
 id BIGINT PRIMARY KEY, name VARCHAR(100) NOT NULL, face DECIMAL(14,2) NOT NULL,
 minimum DECIMAL(14,2) NOT NULL, valid_days INT NOT NULL DEFAULT 30,
 enabled BOOLEAN NOT NULL DEFAULT TRUE, updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6)
);
INSERT IGNORE INTO marketing.promo_coupon_templates (id,name,face,minimum) VALUES (1,'满10元减1元',1.00,10.00);
CREATE TABLE IF NOT EXISTS marketing.promo_coupon_batches (
 id BIGINT PRIMARY KEY, owner_id BIGINT NOT NULL, request_id VARCHAR(64) NOT NULL,
 request_hash CHAR(64) NOT NULL, order_id BIGINT NOT NULL, coupon_id BIGINT NOT NULL DEFAULT 0,
 name VARCHAR(100) NOT NULL, mode VARCHAR(16) NOT NULL, source VARCHAR(16) NOT NULL,
 quantity INT NOT NULL, face DECIMAL(14,2) NOT NULL, minimum DECIMAL(14,2) NOT NULL,
 point_cost BIGINT NOT NULL, claim_points BIGINT NOT NULL DEFAULT 0,
 balance_cost DECIMAL(14,2) NOT NULL, refunded_points BIGINT NOT NULL DEFAULT 0,
 refunded_balance DECIMAL(14,2) NOT NULL DEFAULT 0, rule_snapshot JSON NOT NULL,
 recipients JSON NOT NULL, status VARCHAR(24) NOT NULL, expires_at DATETIME(6) NOT NULL,
 created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6), updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 UNIQUE KEY uk_promo_coupon_request(owner_id,request_id), UNIQUE KEY uk_promo_coupon_order(order_id),
 INDEX idx_promo_coupon_owner(owner_id,created_at,id), INDEX idx_promo_coupon_status(status,expires_at,id)
);
CREATE TABLE IF NOT EXISTS marketing.promo_coupon_units (
 id BIGINT PRIMARY KEY, batch_id BIGINT NOT NULL, recipient_id BIGINT NOT NULL DEFAULT 0,
 user_coupon_id BIGINT NULL, status VARCHAR(24) NOT NULL DEFAULT 'stock',
 owner_points BIGINT NOT NULL, recipient_points BIGINT NOT NULL DEFAULT 0,
 owner_balance DECIMAL(14,2) NOT NULL DEFAULT 0, debit_ref VARCHAR(64) NOT NULL,
 claim_ref VARCHAR(64) NOT NULL DEFAULT '', order_id BIGINT NULL, discount_amount DECIMAL(14,2) NOT NULL DEFAULT 0,
 created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6), updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 UNIQUE KEY uk_promo_coupon_instance(user_coupon_id), INDEX idx_promo_coupon_stock(batch_id,status,id),
 INDEX idx_promo_coupon_recipient(batch_id,recipient_id), INDEX idx_promo_coupon_used(order_id)
);
CREATE TABLE IF NOT EXISTS marketing.promo_coupon_events (
 id BIGINT PRIMARY KEY, batch_id BIGINT NOT NULL, unit_id BIGINT NOT NULL DEFAULT 0,
 actor_id BIGINT NOT NULL, actor_role VARCHAR(16) NOT NULL, operator_ip VARCHAR(64) NOT NULL,
 event VARCHAR(32) NOT NULL, request_id VARCHAR(100) NOT NULL UNIQUE,
 amount DECIMAL(14,2) NOT NULL DEFAULT 0, points BIGINT NOT NULL DEFAULT 0,
 notes VARCHAR(500) NOT NULL, details JSON NULL, created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 INDEX idx_promo_coupon_audit(batch_id,created_at,id)
);
CREATE TABLE IF NOT EXISTS marketing.promo_coupon_tasks (
 id BIGINT PRIMARY KEY, task_key VARCHAR(100) NOT NULL UNIQUE, batch_id BIGINT NOT NULL,
 kind VARCHAR(24) NOT NULL, status VARCHAR(16) NOT NULL DEFAULT 'pending',
 attempts INT NOT NULL DEFAULT 0, next_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 last_error VARCHAR(500) NOT NULL DEFAULT '', created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6), INDEX idx_promo_coupon_task(status,next_at,id)
);
CREATE TABLE IF NOT EXISTS marketing.promo_coupon_notifications (
 id BIGINT PRIMARY KEY, batch_id BIGINT NOT NULL, customer_id BIGINT NOT NULL,
 title VARCHAR(100) NOT NULL, content VARCHAR(500) NOT NULL, delivered BOOLEAN NOT NULL DEFAULT FALSE,
 created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6), UNIQUE KEY uk_promo_coupon_notice(batch_id,customer_id)
);
CREATE TABLE IF NOT EXISTS marketing.promo_coupon_reconciliations (
 id BIGINT PRIMARY KEY, day DATE NOT NULL, batch_id BIGINT NOT NULL, healthy BOOLEAN NOT NULL,
 details JSON NOT NULL, created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 UNIQUE KEY uk_promo_coupon_reconcile(day,batch_id)
);
INSERT INTO system.navigation_nodes (id,scope,parent_id,name,href,target,icon,gate,enabled,sort,created_at,updated_at)
SELECT 7831060800351,'admin',parent_id,'推广客户优惠券','/admin/marketing/promo-coupons','self','GiftIcon','',1,9,NOW(),NOW()
FROM system.navigation_nodes WHERE scope='admin' AND href='/admin/marketing/invites'
AND NOT EXISTS(SELECT 1 FROM system.navigation_nodes WHERE scope='admin' AND href='/admin/marketing/promo-coupons') ORDER BY id LIMIT 1;
UPDATE system.navigation_scopes SET revision=revision+1 WHERE scope='admin';
CREATE TABLE IF NOT EXISTS marketing.promo_coupon_permissions (
 admin_id BIGINT PRIMARY KEY, can_view BOOLEAN NOT NULL DEFAULT FALSE,
 can_review BOOLEAN NOT NULL DEFAULT FALSE, can_close BOOLEAN NOT NULL DEFAULT FALSE,
 can_retry BOOLEAN NOT NULL DEFAULT FALSE, can_config BOOLEAN NOT NULL DEFAULT FALSE,
 updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6)
);
