-- Finance execution ledger: one row per paid business action (delivery, renewal,
-- upgrade) for finance-type suppliers. The unique key is the idempotency guard:
-- the same business action can never be executed twice from two writers.
CREATE TABLE IF NOT EXISTS `order`.`execution_events` (
  `id` BIGINT NOT NULL AUTO_INCREMENT,
  `module` VARCHAR(32) NOT NULL DEFAULT '',
  `idempotency_key` VARCHAR(128) NOT NULL,
  `trace_id` VARCHAR(64) NOT NULL DEFAULT '',
  `entity_type` VARCHAR(32) NOT NULL DEFAULT '',
  `entity_uid` VARCHAR(64) NOT NULL DEFAULT '',
  `operation` VARCHAR(64) NOT NULL DEFAULT '',
  `status` VARCHAR(32) NOT NULL DEFAULT 'pending',
  `attempts` INT NOT NULL DEFAULT 0,
  `expected_status` VARCHAR(32) NOT NULL DEFAULT '',
  `error_code` VARCHAR(64) NOT NULL DEFAULT '',
  `error_message` TEXT,
  `payload` MEDIUMTEXT,
  `created_at` DATETIME(3) NOT NULL,
  `updated_at` DATETIME(3) NOT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `execution_events_idempotency` (`module`, `idempotency_key`),
  KEY `execution_events_status` (`module`, `status`, `updated_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
