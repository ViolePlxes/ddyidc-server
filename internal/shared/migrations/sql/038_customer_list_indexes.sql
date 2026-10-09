SET @ddl=(SELECT IF(COUNT(*)=0,'CREATE INDEX idx_customer_created ON `order`.orders (customer_id,created_at)','DO 0') FROM information_schema.STATISTICS WHERE TABLE_SCHEMA='order' AND TABLE_NAME='orders' AND INDEX_NAME='idx_customer_created');
PREPARE customer_list_ddl FROM @ddl;
EXECUTE customer_list_ddl;
DEALLOCATE PREPARE customer_list_ddl;

SET @ddl=(SELECT IF(COUNT(*)=0,'CREATE INDEX idx_customer_created ON payment.recharge_orders (customer_id,created_at)','DO 0') FROM information_schema.STATISTICS WHERE TABLE_SCHEMA='payment' AND TABLE_NAME='recharge_orders' AND INDEX_NAME='idx_customer_created');
PREPARE customer_list_ddl FROM @ddl;
EXECUTE customer_list_ddl;
DEALLOCATE PREPARE customer_list_ddl;
