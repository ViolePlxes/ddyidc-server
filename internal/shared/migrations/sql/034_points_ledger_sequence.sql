-- Random public IDs do not encode ledger insertion order. Keep this internal
-- sequence private and retain timestamps when replaying historical records.
ALTER TABLE system.points_records
  ADD COLUMN ledger_seq BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  ADD UNIQUE KEY uk_points_ledger_seq (ledger_seq),
  MODIFY COLUMN created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  ADD INDEX idx_points_ledger_replay (user_id, created_at, ledger_seq);
