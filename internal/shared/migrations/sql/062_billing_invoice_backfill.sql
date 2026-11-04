-- A deterministic identity permits safe replay without colliding with new IDs.
INSERT IGNORE INTO billing.payable_invoices
 (id,invoice_no,customer_id,source_type,source_id,revision,source_no,amount_due,amount_paid,status,historical,integrity,created_at,paid_at,updated_at)
 SELECT CONCAT('IH',SHA2(CONCAT('order:',o.id),256)),CONCAT('BILL-O-',o.order_no),o.customer_id,'order',o.id,1,o.order_no,
 CAST(o.pay_amount AS DECIMAL(20,2))/100,IF(o.status IN(2,4,5,6),CAST(o.pay_amount AS DECIMAL(20,2))/100,0),
 CASE o.status WHEN 1 THEN 'open' WHEN 2 THEN 'paid' WHEN 6 THEN 'paid' WHEN 3 THEN 'void' WHEN 4 THEN 'refunded' WHEN 5 THEN 'partial_refund' ELSE 'review' END,
 1,'historical_source',o.created_at,o.paid_at,o.updated_at FROM `order`.orders o WHERE o.customer_id>0;
INSERT IGNORE INTO billing.payable_invoices
 (id,invoice_no,customer_id,source_type,source_id,revision,source_no,amount_due,amount_paid,status,historical,integrity,created_at,paid_at,updated_at)
 SELECT CONCAT('IH',SHA2(CONCAT('recharge:',r.id),256)),CONCAT('BILL-R-',r.recharge_no),r.customer_id,'recharge',r.id,1,r.recharge_no,
 CAST(r.amount AS DECIMAL(20,2))/100,IF(r.status=2,CAST(r.amount AS DECIMAL(20,2))/100,0),
 CASE r.status WHEN 1 THEN 'open' WHEN 2 THEN 'paid' WHEN 3 THEN 'failed' WHEN 4 THEN 'void' ELSE 'review' END,
 1,'historical_source',r.created_at,r.paid_at,r.updated_at FROM payment.recharge_orders r WHERE r.customer_id>0;
INSERT IGNORE INTO billing.payable_invoices
 (id,invoice_no,customer_id,source_type,source_id,revision,source_no,amount_due,amount_paid,status,historical,integrity,created_at,paid_at,updated_at)
 SELECT CONCAT('IH',SHA2(CONCAT('marketplace:',m.id),256)),CONCAT('BILL-M-',m.order_no),m.buyer_id,'marketplace',m.id,1,m.order_no,
 CAST(m.pay_amount AS DECIMAL(14,2)),IF(m.status IN('paid','settled','refund_pending','refunded'),CAST(m.pay_amount AS DECIMAL(14,2)),0),
 CASE WHEN m.status='pending_pay' THEN 'open' WHEN m.status IN('paid','settled','refund_pending') THEN 'paid' WHEN m.status='refunded' THEN 'refunded' WHEN m.status IN('cancelled','expired') THEN 'void' ELSE 'review' END,
 1,'historical_source',m.created_at,m.paid_at,m.updated_at FROM system.marketplace_orders m WHERE m.buyer_id>0
 AND NOT EXISTS(SELECT 1 FROM `order`.orders o WHERE o.id=m.id);
-- Groups without a delivery order have already paid at join time. Require the
-- original matching ledger debit; missing proof is review, never a new charge.
INSERT IGNORE INTO billing.payable_invoices
 (id,invoice_no,customer_id,source_type,source_id,revision,source_no,amount_due,amount_paid,status,historical,integrity,created_at,paid_at,updated_at)
 SELECT CONCAT('IH',SHA2(CONCAT('groupbuy:',g.id),256)),CONCAT('BILL-GB-',g.id),g.client_id,'groupbuy',g.id,1,CONCAT('GB',g.id),
 CAST(g.pay_amount AS DECIMAL(20,2))/100,IF(t.proof_count=1 OR g.pay_amount=0,CAST(g.pay_amount AS DECIMAL(20,2))/100,0),
 IF(t.proof_count=1 OR g.pay_amount=0,IF(g.status=3,'refunded','paid'),'review'),1,'historical_group_ledger',g.created_at,t.paid_at,g.updated_at
 FROM system.groupbuy_records g LEFT JOIN
 (SELECT related_id,customer_id,amount,COUNT(*) proof_count,MIN(created_at) paid_at FROM billing.transactions WHERE related_type='groupbuy' AND type='order_pay' AND channel='groupbuy' GROUP BY related_id,customer_id,amount)t
 ON t.related_id=g.id AND t.customer_id=g.client_id AND t.amount=-CAST(g.pay_amount AS DECIMAL(20,2))/100
 WHERE g.client_id>0 AND g.order_id IS NULL;
INSERT IGNORE INTO billing.invoice_bindings(source_type,source_id,invoice_id)
 SELECT source_type,source_id,id FROM billing.payable_invoices WHERE revision=1;
INSERT IGNORE INTO billing.invoice_items(id,invoice_id,source_item_id,description,module_type,quantity,unit_amount,amount)
 SELECT CONCAT(i.id,':oi:',oi.id),i.id,oi.id,LEFT(oi.product_name,512),oi.module_type,NULLIF(oi.qty,0),
 CAST(oi.unit_price AS DECIMAL(20,2))/100,CAST(oi.subtotal AS DECIMAL(20,2))/100
 FROM billing.payable_invoices i JOIN `order`.order_items oi ON i.source_type='order' AND i.source_id=oi.order_id WHERE i.historical=1;
INSERT IGNORE INTO billing.invoice_items(id,invoice_id,description,module_type,quantity,unit_amount,amount)
 SELECT CONCAT(i.id,':adjust'),i.id,'优惠与费用调整','adjustment',1,
 i.amount_due-COALESCE(a.total,0),i.amount_due-COALESCE(a.total,0)
 FROM billing.payable_invoices i LEFT JOIN(SELECT invoice_id,SUM(amount) total FROM billing.invoice_items GROUP BY invoice_id)a ON a.invoice_id=i.id
 WHERE i.source_type='order' AND i.historical=1 AND i.amount_due<>COALESCE(a.total,0);
INSERT IGNORE INTO billing.invoice_items(id,invoice_id,description,module_type,quantity,unit_amount,amount)
 SELECT CONCAT(id,':recharge'),id,'账户充值','recharge',1,amount_due,amount_due FROM billing.payable_invoices WHERE source_type='recharge' AND historical=1;
INSERT IGNORE INTO billing.invoice_items(id,invoice_id,description,module_type,quantity,unit_amount,amount)
 SELECT CONCAT(id,':market'),id,'资源交易','marketplace',1,amount_due,amount_due FROM billing.payable_invoices WHERE source_type='marketplace' AND historical=1;
INSERT IGNORE INTO billing.invoice_items(id,invoice_id,description,module_type,quantity,unit_amount,amount)
 SELECT CONCAT(id,':group'),id,'云服务器拼团','groupbuy',1,amount_due,amount_due FROM billing.payable_invoices WHERE source_type='groupbuy' AND historical=1;
INSERT IGNORE INTO billing.invoice_payment_links(payment_id,invoice_id,amount,currency,created_at)
 SELECT p.id,b.invoice_id,CAST(p.amount_fen AS DECIMAL(20,2))/100,p.currency,p.created_at
 FROM payment.payments p JOIN billing.invoice_bindings b ON b.source_id=p.biz_id
 AND b.source_type=CASE WHEN p.biz_type='marketplace' AND EXISTS(SELECT 1 FROM `order`.orders o WHERE o.id=p.biz_id) THEN 'order' ELSE p.biz_type END
 JOIN billing.payable_invoices i ON i.id=b.invoice_id AND i.customer_id=p.customer_id
 WHERE p.amount_fen>0 AND p.currency='CNY';
-- Received funds under review are never advertised as payable again.
UPDATE billing.payable_invoices i JOIN billing.invoice_payment_links l ON l.invoice_id=i.id
 JOIN payment.payments p ON p.id=l.payment_id
 SET i.status='review',i.integrity='receipt_review'
 WHERE p.status='received_review';
INSERT IGNORE INTO billing.invoice_events(invoice_id,event_key,event_type,reference_type,reference_id,created_at)
 SELECT id,CONCAT('backfill:',id),'historical_backfill',source_type,CAST(source_id AS CHAR),CURRENT_TIMESTAMP(6)
 FROM billing.payable_invoices WHERE historical=1;

-- INSERT IGNORE is used only for replay identity. These mandatory assertions
-- prevent ignored invalid/overflow rows or missing line items from succeeding.
INSERT INTO billing.invoice_migration_checks(check_name,expected,actual)
 SELECT 'order_sources',(SELECT COUNT(*) FROM `order`.orders WHERE customer_id>0),
 (SELECT COUNT(*) FROM billing.payable_invoices WHERE source_type='order' AND historical=1 AND revision=1)
 ON DUPLICATE KEY UPDATE expected=VALUES(expected),actual=VALUES(actual),checked_at=CURRENT_TIMESTAMP(6);
-- Preserve source prices exactly; INSERT IGNORE must never silently clamp them.
INSERT INTO billing.invoice_migration_checks(check_name,expected,actual)
 SELECT 'order_amounts',0,COUNT(*) FROM billing.payable_invoices i JOIN `order`.orders o ON i.source_id=o.id
 WHERE i.source_type='order' AND i.historical=1 AND (i.customer_id<>o.customer_id OR i.amount_due<>CAST(o.pay_amount AS DECIMAL(20,2))/100)
 ON DUPLICATE KEY UPDATE expected=VALUES(expected),actual=VALUES(actual),checked_at=CURRENT_TIMESTAMP(6);
INSERT INTO billing.invoice_migration_checks(check_name,expected,actual)
 SELECT 'recharge_amounts',0,COUNT(*) FROM billing.payable_invoices i JOIN payment.recharge_orders r ON i.source_id=r.id
 WHERE i.source_type='recharge' AND i.historical=1 AND (i.customer_id<>r.customer_id OR i.amount_due<>CAST(r.amount AS DECIMAL(20,2))/100)
 ON DUPLICATE KEY UPDATE expected=VALUES(expected),actual=VALUES(actual),checked_at=CURRENT_TIMESTAMP(6);
INSERT INTO billing.invoice_migration_checks(check_name,expected,actual)
 SELECT 'recharge_sources',(SELECT COUNT(*) FROM payment.recharge_orders WHERE customer_id>0),
 (SELECT COUNT(*) FROM billing.payable_invoices WHERE source_type='recharge' AND historical=1 AND revision=1)
 ON DUPLICATE KEY UPDATE expected=VALUES(expected),actual=VALUES(actual),checked_at=CURRENT_TIMESTAMP(6);
INSERT INTO billing.invoice_migration_checks(check_name,expected,actual)
 SELECT 'marketplace_sources',(SELECT COUNT(*) FROM system.marketplace_orders m WHERE m.buyer_id>0 AND NOT EXISTS(SELECT 1 FROM `order`.orders o WHERE o.id=m.id)),
 (SELECT COUNT(*) FROM billing.payable_invoices WHERE source_type='marketplace' AND historical=1 AND revision=1)
 ON DUPLICATE KEY UPDATE expected=VALUES(expected),actual=VALUES(actual),checked_at=CURRENT_TIMESTAMP(6);
INSERT INTO billing.invoice_migration_checks(check_name,expected,actual)
 SELECT 'line_amounts',0,COUNT(*) FROM billing.payable_invoices i LEFT JOIN
 (SELECT invoice_id,SUM(amount) total FROM billing.invoice_items GROUP BY invoice_id)a ON a.invoice_id=i.id
 WHERE i.historical=1 AND i.amount_due<>COALESCE(a.total,0)
 ON DUPLICATE KEY UPDATE expected=VALUES(expected),actual=VALUES(actual),checked_at=CURRENT_TIMESTAMP(6);
INSERT INTO billing.invoice_migration_checks(check_name,expected,actual)
 SELECT 'source_bindings',0,COUNT(*) FROM billing.payable_invoices i LEFT JOIN billing.invoice_bindings b
 ON b.source_type=i.source_type AND b.source_id=i.source_id AND b.invoice_id=i.id
 WHERE i.historical=1 AND b.invoice_id IS NULL
 ON DUPLICATE KEY UPDATE expected=VALUES(expected),actual=VALUES(actual),checked_at=CURRENT_TIMESTAMP(6);

INSERT INTO billing.invoice_migration_checks(check_name,expected,actual)
 SELECT 'groupbuy_sources',(SELECT COUNT(*) FROM system.groupbuy_records WHERE client_id>0 AND order_id IS NULL),
 (SELECT COUNT(*) FROM billing.payable_invoices WHERE source_type='groupbuy' AND historical=1 AND revision=1)
 ON DUPLICATE KEY UPDATE expected=VALUES(expected),actual=VALUES(actual),checked_at=CURRENT_TIMESTAMP(6);
