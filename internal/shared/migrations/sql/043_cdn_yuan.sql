ALTER TABLE system.cdnsell_products MODIFY price DECIMAL(14,2) NOT NULL;
UPDATE system.cdnsell_products SET price=price/100;
UPDATE system.cdnsell_products p JOIN (
 SELECT p2.id,JSON_OBJECTAGG(k.tier,CAST(JSON_UNQUOTE(JSON_EXTRACT(p2.tier_prices,CONCAT('$.' ,JSON_QUOTE(k.tier)))) AS DECIMAL(14,2))/100) AS prices
 FROM system.cdnsell_products p2 JOIN JSON_TABLE(JSON_KEYS(p2.tier_prices),'$[*]' COLUMNS(tier VARCHAR(64) PATH '$')) k
 GROUP BY p2.id
) converted ON converted.id=p.id SET p.tier_prices=converted.prices;
ALTER TABLE system.cdnsell_settings MODIFY overage_price DECIMAL(14,2) NOT NULL DEFAULT 0;
UPDATE system.cdnsell_settings SET overage_price=overage_price/100;
ALTER TABLE system.cdnsell_overage_bills MODIFY unit_price DECIMAL(14,2) NOT NULL,MODIFY amount DECIMAL(14,2) NOT NULL;
UPDATE system.cdnsell_overage_bills SET unit_price=unit_price/100,amount=amount/100;
ALTER TABLE system.cdnsell_orders MODIFY amount DECIMAL(14,2) NOT NULL;
UPDATE system.cdnsell_orders SET amount=amount/100;
