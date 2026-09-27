-- Channel receipt facts are independent of order fulfillment and local expiry.
CREATE TABLE IF NOT EXISTS payment.receipts (
 payment_id BIGINT NOT NULL PRIMARY KEY,
 channel VARCHAR(16) NOT NULL,
 transaction_id VARCHAR(128) NOT NULL,
 amount_fen BIGINT NOT NULL,
 currency CHAR(3) NOT NULL,
 paid_at DATETIME(3) NOT NULL,
 created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
 UNIQUE KEY uk_channel_transaction (channel, transaction_id)
) ENGINE=InnoDB;
-- Adopt known historical channel facts. Conflicting transaction identities fail
-- migration for explicit reconciliation instead of silently dropping a receipt.
INSERT INTO payment.receipts (payment_id,channel,transaction_id,amount_fen,currency,paid_at)
 SELECT p.id,p.channel,p.transaction_id,p.amount_fen,p.currency,COALESCE(p.paid_at,p.updated_at)
 FROM payment.payments p LEFT JOIN payment.receipts r ON r.payment_id=p.id
 WHERE p.status IN ('paid','refunded') AND p.transaction_id IS NOT NULL
 AND p.transaction_id<>'' AND p.amount_fen>0 AND r.payment_id IS NULL;
CREATE TABLE IF NOT EXISTS payment.receipt_reviews (
 payment_id BIGINT NOT NULL PRIMARY KEY,
 reason VARCHAR(64) NOT NULL,
 status VARCHAR(16) NOT NULL DEFAULT 'pending',
 created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
 resolved_at DATETIME(3) NULL
) ENGINE=InnoDB;
