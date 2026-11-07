CREATE TABLE IF NOT EXISTS system.domain_dns_notes (
 id BIGINT PRIMARY KEY, customer_id BIGINT NOT NULL, domain VARCHAR(253) NOT NULL, record_id VARCHAR(64) NOT NULL,
 remark VARCHAR(255) NOT NULL DEFAULT '', created_at DATETIME(3) NOT NULL, updated_at DATETIME(3) NOT NULL,
 UNIQUE KEY domain_dns_note_record(domain,record_id), INDEX domain_dns_note_owner(customer_id,domain)
);
