CREATE TABLE IF NOT EXISTS ticket.upload_owners (
  owner_type VARCHAR(16) NOT NULL, owner_id BIGINT NOT NULL,
  PRIMARY KEY(owner_type,owner_id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS ticket.uploads (
  id CHAR(36) NOT NULL PRIMARY KEY,
  owner_type VARCHAR(16) NOT NULL, owner_id BIGINT NOT NULL,
  name VARCHAR(255) NOT NULL, size BIGINT NOT NULL, content_type VARCHAR(100) NOT NULL,
  sha256 CHAR(64) NOT NULL DEFAULT '',
  state VARCHAR(16) NOT NULL, ticket_id BIGINT NULL,
  created_at DATETIME(6) NOT NULL, expires_at DATETIME(6) NOT NULL,
  INDEX owner_created(owner_type,owner_id,created_at),
  INDEX expiry(state,expires_at), INDEX binding(ticket_id)
) ENGINE=InnoDB;
