-- Optional operating system family for an operator-maintained product spec.
ALTER TABLE product.product_specs ADD COLUMN os_family VARCHAR(16) NOT NULL DEFAULT '' AFTER memory;
