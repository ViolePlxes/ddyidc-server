USE product;
-- Run while the application is stopped. Back up product.product_specs first.
-- Legacy rows are archived, then the first row by sort/id becomes the single
-- product specification. Existing orders keep their immutable configuration.
-- Review affected products and outstanding legacy carts before this migration.
CREATE TABLE IF NOT EXISTS product.product_specs_before_single LIKE product.product_specs;
INSERT IGNORE INTO product.product_specs_before_single SELECT * FROM product.product_specs;
START TRANSACTION;
DELETE later FROM product.product_specs later
JOIN product.product_specs earlier ON earlier.product_id=later.product_id
AND (earlier.sort<later.sort OR (earlier.sort=later.sort AND earlier.id<later.id));
COMMIT;
SET @single_spec_sql = IF(
 (SELECT COUNT(*) FROM information_schema.statistics WHERE table_schema='product' AND table_name='product_specs' AND index_name='uk_product_spec')=0,
 'ALTER TABLE product.product_specs ADD UNIQUE KEY uk_product_spec(product_id)', 'SELECT 1');
PREPARE single_spec_stmt FROM @single_spec_sql;
EXECUTE single_spec_stmt;
DEALLOCATE PREPARE single_spec_stmt;
CREATE TABLE IF NOT EXISTS product.product_spec_snapshots (
 product_id BIGINT UNSIGNED NOT NULL PRIMARY KEY,
 data JSON NOT NULL,
 captured_at DATETIME NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
