CREATE TABLE IF NOT EXISTS system.navigation_scopes (
 scope VARCHAR(16) PRIMARY KEY,
 revision BIGINT NOT NULL DEFAULT 0,
 seeded BOOLEAN NOT NULL DEFAULT FALSE
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS system.navigation_nodes (
 id BIGINT PRIMARY KEY,
 scope VARCHAR(16) NOT NULL,
 parent_id BIGINT NOT NULL DEFAULT 0,
 name VARCHAR(80) NOT NULL,
 href VARCHAR(2048) NOT NULL DEFAULT '',
 target VARCHAR(8) NOT NULL DEFAULT 'self',
 icon VARCHAR(80) NOT NULL DEFAULT 'FolderIcon',
 gate VARCHAR(2048) NOT NULL DEFAULT '',
 category VARCHAR(32) NOT NULL DEFAULT '',
 enabled BOOLEAN NOT NULL DEFAULT TRUE,
 sort INT NOT NULL DEFAULT 0,
 created_at DATETIME(3) NOT NULL,
 updated_at DATETIME(3) NOT NULL,
 INDEX navigation_level (scope,parent_id,sort,id)
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS system.navigation_categories (
 id BIGINT PRIMARY KEY,
 name VARCHAR(32) NOT NULL,
 sort INT NOT NULL DEFAULT 0,
 created_at DATETIME(3) NOT NULL,
 updated_at DATETIME(3) NOT NULL,
 UNIQUE KEY navigation_category_name (name)
) ENGINE=InnoDB;
