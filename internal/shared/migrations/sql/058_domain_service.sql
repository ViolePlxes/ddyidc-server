CREATE TABLE IF NOT EXISTS system.domain_settings (
 id TINYINT PRIMARY KEY, enabled BOOLEAN NOT NULL DEFAULT TRUE, username VARCHAR(128) NOT NULL DEFAULT '', password_cipher TEXT NOT NULL,
 markup_bps INT NOT NULL DEFAULT 0, service_fee DECIMAL(14,2) NOT NULL DEFAULT 0, redemption_cost DECIMAL(14,2) NOT NULL DEFAULT 0,
 revision BIGINT NOT NULL DEFAULT 1, updated_at DATETIME(3) NOT NULL
);
INSERT IGNORE INTO system.domain_settings(id,password_cipher,updated_at) VALUES(1,'',NOW());
CREATE TABLE IF NOT EXISTS system.domain_assets (
 id BIGINT PRIMARY KEY, customer_id BIGINT NOT NULL, domain VARCHAR(253) NOT NULL, status VARCHAR(24) NOT NULL,
 expires_at DATETIME(3) NULL, created_at DATETIME(3) NOT NULL, updated_at DATETIME(3) NOT NULL,
 UNIQUE KEY domain_asset_name(domain), INDEX domain_asset_owner(customer_id,id)
);
CREATE TABLE IF NOT EXISTS system.domain_templates (
 id BIGINT PRIMARY KEY, customer_id BIGINT NOT NULL, provider_id VARCHAR(64) NOT NULL, label VARCHAR(120) NOT NULL,
 status VARCHAR(24) NOT NULL DEFAULT 'submitted', created_at DATETIME(3) NOT NULL, updated_at DATETIME(3) NOT NULL,
 UNIQUE KEY domain_template_remote(provider_id), INDEX domain_template_owner(customer_id,id)
);
CREATE TABLE IF NOT EXISTS system.domain_operations (
 id BIGINT PRIMARY KEY, customer_id BIGINT NOT NULL, action VARCHAR(32) NOT NULL, domain VARCHAR(253) NOT NULL DEFAULT '',
 request_id VARCHAR(80) NOT NULL, input_hash CHAR(64) NOT NULL, status VARCHAR(24) NOT NULL,
 result JSON NULL, error_code VARCHAR(32) NOT NULL DEFAULT '', created_at DATETIME(3) NOT NULL, updated_at DATETIME(3) NOT NULL,
 UNIQUE KEY domain_operation_request(customer_id,request_id), INDEX domain_operation_owner(customer_id,created_at), INDEX domain_operation_guard(domain,action,status)
);
CREATE TABLE IF NOT EXISTS system.domain_quotes (
 id BIGINT PRIMARY KEY, customer_id BIGINT NOT NULL, action VARCHAR(32) NOT NULL, domain VARCHAR(253) NOT NULL,
 payload_cipher MEDIUMTEXT NOT NULL, cost DECIMAL(14,2) NOT NULL, amount DECIMAL(14,2) NOT NULL, config_revision BIGINT NOT NULL,
 expires_at DATETIME(3) NOT NULL, order_id BIGINT NULL, created_at DATETIME(3) NOT NULL, INDEX domain_quote_owner(customer_id,id)
);
CREATE TABLE IF NOT EXISTS system.domain_orders (
 id BIGINT PRIMARY KEY, customer_id BIGINT NOT NULL, quote_id BIGINT NOT NULL, action VARCHAR(32) NOT NULL, domain VARCHAR(253) NOT NULL,
 payload_cipher MEDIUMTEXT NOT NULL, cost DECIMAL(14,2) NOT NULL, amount DECIMAL(14,2) NOT NULL,
 status VARCHAR(24) NOT NULL DEFAULT 'pending', error_code VARCHAR(64) NOT NULL DEFAULT '', created_at DATETIME(3) NOT NULL, updated_at DATETIME(3) NOT NULL,
 UNIQUE KEY domain_order_quote(quote_id), INDEX domain_order_owner(customer_id,id)
);
CREATE TABLE IF NOT EXISTS system.domain_admin_audits (
 id BIGINT PRIMARY KEY, admin_id BIGINT NOT NULL, operator_ip VARCHAR(128) NOT NULL, notes VARCHAR(500) NOT NULL,
 action VARCHAR(48) NOT NULL, created_at DATETIME(3) NOT NULL
);
INSERT IGNORE INTO system.notification_templates(id,scene,channel,title,content,enabled) VALUES
 (950001001,'domain_order_success','both','域名订单已完成','您的域名 {{domain}} 订单 {{order_no}} 已完成。',1),
 (950001002,'domain_order_failed','both','域名订单需要核对','您的域名 {{domain}} 订单 {{order_no}} 尚未完成，请查看订单记录或联系客服。',1);

INSERT INTO system.navigation_nodes(id,scope,parent_id,name,href,target,icon,gate,enabled,sort,created_at,updated_at)
SELECT 950001100,'admin',parent_id,'域名服务','/admin/domains','self','GlobeIcon','',1,9,NOW(),NOW()
FROM system.navigation_nodes WHERE scope='admin' AND href='/admin/system'
AND EXISTS(SELECT 1 FROM system.navigation_scopes WHERE scope='admin' AND seeded=1)
AND NOT EXISTS(SELECT 1 FROM system.navigation_nodes WHERE id=950001100 OR (scope='admin' AND href='/admin/domains')) LIMIT 1;
UPDATE system.navigation_scopes SET revision=revision+1 WHERE scope='admin' AND seeded=1;

CREATE TABLE IF NOT EXISTS system.domain_rate_limits (bucket VARCHAR(80) PRIMARY KEY,window_start BIGINT NOT NULL,used INT NOT NULL);
