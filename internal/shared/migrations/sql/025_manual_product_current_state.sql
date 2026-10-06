CREATE TABLE IF NOT EXISTS system.manual_product_todos (
 source VARCHAR(16) NOT NULL,
 object_id BIGINT NOT NULL,
 revision CHAR(64) NOT NULL,
 state VARCHAR(16) NOT NULL,
 ip_address VARCHAR(45) NOT NULL DEFAULT '',
 PRIMARY KEY(source,object_id)
) ENGINE=InnoDB;

-- Adopt existing cleared items as current state; do not create todo history.
INSERT INTO system.manual_product_todos(source,object_id,revision,state,ip_address)
SELECT a.source,a.object_id,a.evidence,'processed',''
FROM system.todo_audits a
WHERE a.kind='manual-products' AND a.action='mark_processed' AND CHAR_LENGTH(a.evidence)=64
 AND NOT EXISTS (SELECT 1 FROM system.todo_audits newer WHERE newer.kind=a.kind AND newer.source=a.source AND newer.object_id=a.object_id AND newer.action=a.action AND (newer.created_at>a.created_at OR (newer.created_at=a.created_at AND newer.id>a.id)))
ON DUPLICATE KEY UPDATE revision=VALUES(revision),state=VALUES(state);
