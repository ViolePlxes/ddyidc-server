SET @ddl=(SELECT IF(COUNT(*)=0,'CREATE INDEX idx_customer_status_created ON `order`.orders (customer_id,status,created_at)','DO 0') FROM information_schema.STATISTICS WHERE TABLE_SCHEMA='order' AND TABLE_NAME='orders' AND INDEX_NAME='idx_customer_status_created');
PREPARE customer_status_ddl FROM @ddl;
EXECUTE customer_status_ddl;
DEALLOCATE PREPARE customer_status_ddl;
