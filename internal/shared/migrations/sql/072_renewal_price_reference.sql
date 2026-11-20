-- Finance-style product price reference: the latest verified supplier original
-- price per (supplier, product, cycle). The finance system keeps renewal prices
-- on the product; this table is the read-only equivalent for finance suppliers so
-- a machine without a fresh per-host quote still has a reference price instead of
-- dropping out of the admin total.
--
-- Written only by the supplier snapshot sync for suppliers of type finance/default.
-- Never read by customer pricing paths.
CREATE TABLE IF NOT EXISTS `instance`.`renewal_price_reference` (
  `id` BIGINT NOT NULL AUTO_INCREMENT,
  `supplier_id` BIGINT NOT NULL,
  `product_id` BIGINT NOT NULL,
  `cycle` VARCHAR(32) NOT NULL,
  `amount` DECIMAL(14,2) NOT NULL,
  `verified_at` DATETIME(3) NOT NULL,
  `created_at` DATETIME(3) NOT NULL,
  `updated_at` DATETIME(3) NOT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `renewal_price_reference_product` (`supplier_id`, `product_id`, `cycle`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
