ALTER TABLE system.cdnsell_usage_windows ADD COLUMN unit_price DECIMAL(14,2) NOT NULL DEFAULT 0;
ALTER TABLE system.cdnsell_overage_bills ADD UNIQUE KEY uk_cdnsell_bill_window(domain_id,start_time,end_time,unit_price);
ALTER TABLE system.cdnsell_orders ADD UNIQUE KEY uk_cdnsell_bill_order(bill_id);
