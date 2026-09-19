CREATE TABLE IF NOT EXISTS system.push_transfers (
 id BIGINT NOT NULL PRIMARY KEY,
 instance_id BIGINT NOT NULL,
 from_customer_id BIGINT NOT NULL,
 to_customer_id BIGINT NOT NULL,
 source_order_id BIGINT NOT NULL DEFAULT 0,
 market_order_id BIGINT NOT NULL DEFAULT 0,
 request_key VARCHAR(100) NOT NULL,
 source VARCHAR(32) NOT NULL,
 status VARCHAR(24) NOT NULL,
 operator_ip VARCHAR(64) NOT NULL DEFAULT '',
 receiver_ip VARCHAR(64) NOT NULL DEFAULT '',
 risk_flag VARCHAR(100) NOT NULL DEFAULT '',
 created_at DATETIME(3) NOT NULL,
 updated_at DATETIME(3) NOT NULL,
 pending_instance BIGINT GENERATED ALWAYS AS (CASE WHEN status='pending' THEN instance_id ELSE NULL END) STORED,
 UNIQUE KEY uk_push_request(request_key),
 UNIQUE KEY uk_push_pending(pending_instance),
 KEY idx_push_from(from_customer_id,created_at),
 KEY idx_push_to(to_customer_id,status),
 KEY idx_push_instance(instance_id,created_at),
 KEY idx_push_market(market_order_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

SET @push017_sql = IF((SELECT COUNT(*) FROM information_schema.columns WHERE table_schema='instance' AND table_name='instances' AND column_name='ownership_version')=0, 'ALTER TABLE instance.instances ADD COLUMN ownership_version BIGINT NOT NULL DEFAULT 0', 'SELECT 1');
PREPARE push017_stmt FROM @push017_sql; EXECUTE push017_stmt; DEALLOCATE PREPARE push017_stmt;
SET @push017_sql = IF((SELECT COUNT(*) FROM information_schema.columns WHERE table_schema='system' AND table_name='marketplace_settings' AND column_name='auto_confirm_hours')=0, 'ALTER TABLE system.marketplace_settings ADD COLUMN auto_confirm_hours INT NOT NULL DEFAULT 24', 'SELECT 1');
PREPARE push017_stmt FROM @push017_sql; EXECUTE push017_stmt; DEALLOCATE PREPARE push017_stmt;
SET @push017_sql = IF((SELECT COUNT(*) FROM information_schema.columns WHERE table_schema='system' AND table_name='marketplace_orders' AND column_name='settlement_mode')=0, 'ALTER TABLE system.marketplace_orders ADD COLUMN settlement_mode VARCHAR(24) NOT NULL DEFAULT ''legacy''', 'SELECT 1');
PREPARE push017_stmt FROM @push017_sql; EXECUTE push017_stmt; DEALLOCATE PREPARE push017_stmt;
SET @push017_sql = IF((SELECT COUNT(*) FROM information_schema.columns WHERE table_schema='system' AND table_name='marketplace_orders' AND column_name='operator_ip')=0, 'ALTER TABLE system.marketplace_orders ADD COLUMN operator_ip VARCHAR(64) NOT NULL DEFAULT ''''', 'SELECT 1');
PREPARE push017_stmt FROM @push017_sql; EXECUTE push017_stmt; DEALLOCATE PREPARE push017_stmt;
SET @push017_sql = IF((SELECT COUNT(*) FROM information_schema.columns WHERE table_schema='system' AND table_name='marketplace_listings' AND column_name='risk_flag')=0, 'ALTER TABLE system.marketplace_listings ADD COLUMN risk_flag VARCHAR(100) NOT NULL DEFAULT ''''', 'SELECT 1');
PREPARE push017_stmt FROM @push017_sql; EXECUTE push017_stmt; DEALLOCATE PREPARE push017_stmt;
SET @push017_sql = IF((SELECT DATA_TYPE FROM information_schema.columns WHERE table_schema='system' AND table_name='marketplace_orders' AND column_name='status')='enum','ALTER TABLE system.marketplace_orders MODIFY status VARCHAR(32) NOT NULL','SELECT 1');
PREPARE push017_stmt FROM @push017_sql; EXECUTE push017_stmt; DEALLOCATE PREPARE push017_stmt;
SET @push017_sql = IF((SELECT DATA_TYPE FROM information_schema.columns WHERE table_schema='system' AND table_name='marketplace_flows' AND column_name='type')='enum','ALTER TABLE system.marketplace_flows MODIFY type VARCHAR(32) NOT NULL','SELECT 1');
PREPARE push017_stmt FROM @push017_sql; EXECUTE push017_stmt; DEALLOCATE PREPARE push017_stmt;
