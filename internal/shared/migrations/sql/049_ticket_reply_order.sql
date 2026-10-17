-- Internal insertion order only; public opaque IDs and API cursors are unchanged.
-- Existing replies receive a deterministic historical order. MySQL assigns every
-- subsequent reply a sequence, including writes from the previous application.
SET @ticket_seq_ddl=(SELECT IF(COUNT(*)=0,'ALTER TABLE ticket.ticket_reply ADD COLUMN reply_seq BIGINT UNSIGNED NULL','DO 0') FROM information_schema.COLUMNS WHERE TABLE_SCHEMA='ticket' AND TABLE_NAME='ticket_reply' AND COLUMN_NAME='reply_seq');
PREPARE ticket_seq_stmt FROM @ticket_seq_ddl;
EXECUTE ticket_seq_stmt;
DEALLOCATE PREPARE ticket_seq_stmt;
LOCK TABLES ticket.ticket_reply WRITE;
CREATE TEMPORARY TABLE ticket.ticket_reply_sequence_backfill (id BIGINT UNSIGNED PRIMARY KEY, seq BIGINT UNSIGNED NOT NULL);
INSERT INTO ticket.ticket_reply_sequence_backfill SELECT id, ROW_NUMBER() OVER (ORDER BY create_time,id) FROM ticket.ticket_reply;
UPDATE ticket.ticket_reply JOIN ticket.ticket_reply_sequence_backfill b ON b.id=ticket.ticket_reply.id SET ticket.ticket_reply.reply_seq=b.seq WHERE ticket.ticket_reply.reply_seq IS NULL;
DROP TEMPORARY TABLE ticket.ticket_reply_sequence_backfill;
SET @ticket_seq_index=(SELECT IF(COUNT(*)=0,'ALTER TABLE ticket.ticket_reply MODIFY reply_seq BIGINT UNSIGNED NOT NULL AUTO_INCREMENT, ADD UNIQUE KEY uk_reply_seq(reply_seq)','DO 0') FROM information_schema.STATISTICS WHERE TABLE_SCHEMA='ticket' AND TABLE_NAME='ticket_reply' AND INDEX_NAME='uk_reply_seq');
PREPARE ticket_seq_stmt FROM @ticket_seq_index;
EXECUTE ticket_seq_stmt;
DEALLOCATE PREPARE ticket_seq_stmt;
UNLOCK TABLES;
