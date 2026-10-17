-- Versioned rich text is additive; existing replies retain NULL/plain content.
SET @ticket_rich_ddl=(SELECT IF(COUNT(*)=0,'ALTER TABLE ticket.ticket_reply ADD COLUMN rich_content JSON DEFAULT NULL','DO 0') FROM information_schema.COLUMNS WHERE TABLE_SCHEMA='ticket' AND TABLE_NAME='ticket_reply' AND COLUMN_NAME='rich_content');
PREPARE ticket_rich_stmt FROM @ticket_rich_ddl;
EXECUTE ticket_rich_stmt;
DEALLOCATE PREPARE ticket_rich_stmt;
