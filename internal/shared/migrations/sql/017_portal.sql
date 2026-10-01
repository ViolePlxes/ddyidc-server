CREATE TABLE IF NOT EXISTS system.portal_sites (
 id BIGINT PRIMARY KEY, revision BIGINT NOT NULL DEFAULT 0,
 draft LONGTEXT NOT NULL, published LONGTEXT NOT NULL,
 fingerprint VARCHAR(64) NOT NULL DEFAULT '', generation VARCHAR(64) NOT NULL DEFAULT '',
 updated_at DATETIME(6) NOT NULL
);
CREATE TABLE IF NOT EXISTS system.portal_revisions (
 id BIGINT AUTO_INCREMENT PRIMARY KEY, revision BIGINT NOT NULL,
 document LONGTEXT NOT NULL, actor_id BIGINT NOT NULL,
 operator_ip VARCHAR(128) NOT NULL, action VARCHAR(32) NOT NULL,
 created_at DATETIME(6) NOT NULL, INDEX portal_revision_idx(revision)
);
CREATE TABLE IF NOT EXISTS system.portal_media (
 id VARCHAR(64) PRIMARY KEY, name VARCHAR(255) NOT NULL, path VARCHAR(255) NOT NULL,
 mime VARCHAR(64) NOT NULL, actor_id BIGINT NOT NULL, created_at DATETIME(6) NOT NULL
);
INSERT IGNORE INTO system.portal_sites (id,revision,draft,published,updated_at) VALUES (1,0,'','',NOW(6));
INSERT IGNORE INTO system.navigation_nodes (id,scope,parent_id,name,href,target,icon,gate,enabled,sort,created_at,updated_at)
 SELECT 991006001,'admin',id,'门户首页配置','/admin/portal','self','GlobeHemisphereWestIcon','',1,99,NOW(6),NOW(6)
 FROM system.navigation_nodes WHERE scope='admin' AND parent_id=0 AND id=38 LIMIT 1;
