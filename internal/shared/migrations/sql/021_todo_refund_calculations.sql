-- Calculation evidence is append-only. A pending application's original
-- timestamp and service periods must not be recomputed at review time.
CREATE TABLE IF NOT EXISTS payment.todo_refund_calculations (
 application_id BIGINT UNSIGNED NOT NULL PRIMARY KEY,
 customer_id BIGINT UNSIGNED NOT NULL,
 source VARCHAR(32) NOT NULL,
 object_id BIGINT UNSIGNED NOT NULL,
 amount DECIMAL(14,2) NOT NULL,
 paid_amount DECIMAL(14,2) NOT NULL,
 setup_amount DECIMAL(14,2) NOT NULL,
 applied_at DATETIME(3) NOT NULL,
 calculation JSON NOT NULL,
 created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
 KEY idx_todo_refund_object(source,object_id,application_id),
 KEY idx_todo_refund_customer(customer_id,application_id),
 CONSTRAINT chk_todo_refund_amount CHECK (amount>=0 AND paid_amount>=amount AND setup_amount>=0)
) ENGINE=InnoDB;
