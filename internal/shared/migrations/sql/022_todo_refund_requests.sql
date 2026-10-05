CREATE TABLE IF NOT EXISTS payment.todo_refund_requests (
 application_id BIGINT UNSIGNED NOT NULL PRIMARY KEY,
 source VARCHAR(32) NOT NULL,
 object_id BIGINT UNSIGNED NOT NULL,
 customer_id BIGINT UNSIGNED NOT NULL,
 request_id VARCHAR(64) NOT NULL UNIQUE,
 snapshot JSON NOT NULL,
 created_at DATETIME(3) NOT NULL,
 KEY idx_todo_refund_request_object(source,object_id,application_id),
 KEY idx_todo_refund_request_customer(customer_id,created_at)
) ENGINE=InnoDB;
