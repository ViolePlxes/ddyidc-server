-- Keep the already rehearsed 019 checksum immutable. Registration coupons
-- reuse marketing.invites; no independent commission snapshot is needed.
-- Remove only the unused empty draft table. Preserve any existing records.
SET @todo_unused_batch_rows = (SELECT COUNT(*) FROM marketing.reward_batches);
SET @todo_unused_batch_sql = IF(@todo_unused_batch_rows = 0,
  'DROP TABLE marketing.reward_batches', 'SELECT 1');
PREPARE todo_unused_batch_statement FROM @todo_unused_batch_sql;
EXECUTE todo_unused_batch_statement;
DEALLOCATE PREPARE todo_unused_batch_statement;
