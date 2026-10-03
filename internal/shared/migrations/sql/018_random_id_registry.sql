CREATE TABLE IF NOT EXISTS system.id_registry (
 id BIGINT UNSIGNED PRIMARY KEY,
 allocated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6)
) ENGINE=InnoDB COMMENT='Global unique reservations for opaque cryptographic random IDs';
