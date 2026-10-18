-- Explicit browser acknowledgement, independent of the legacy ticket GET cursor.
CREATE TABLE IF NOT EXISTS system.notification_ticket_reads (
 recipient_id BIGINT UNSIGNED NOT NULL, ticket_id BIGINT UNSIGNED NOT NULL,
 last_reply_seq BIGINT UNSIGNED NOT NULL DEFAULT 0, updated_at DATETIME(6) NOT NULL,
 PRIMARY KEY(recipient_id,ticket_id)
) ENGINE=InnoDB;
