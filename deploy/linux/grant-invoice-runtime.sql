-- Offline DBA after 061/062, for the existing runtime identity only.
-- No DDL, DELETE, balance/account access or configuration writes are granted.
GRANT SELECT ON billing.invoice_settings TO 'ddyidc_runtime'@'127.0.0.1';
GRANT SELECT,INSERT,UPDATE ON billing.payable_invoices TO 'ddyidc_runtime'@'127.0.0.1';
GRANT SELECT,INSERT ON billing.invoice_items TO 'ddyidc_runtime'@'127.0.0.1';
GRANT SELECT,INSERT,UPDATE ON billing.invoice_bindings TO 'ddyidc_runtime'@'127.0.0.1';
GRANT SELECT,INSERT ON billing.invoice_payment_links TO 'ddyidc_runtime'@'127.0.0.1';
GRANT SELECT,INSERT ON billing.invoice_events TO 'ddyidc_runtime'@'127.0.0.1';
GRANT SELECT,INSERT ON billing.invoice_unmatched_receipts TO 'ddyidc_runtime'@'127.0.0.1';
