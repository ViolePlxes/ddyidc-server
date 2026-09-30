-- Run offline as DBA after 016_rewards_center.sql on an existing restricted runtime installation.
-- New installations receive these table grants from database-identities.py.
GRANT SELECT,INSERT ON system.points_events TO 'ddyidc_runtime'@'127.0.0.1';
GRANT SELECT,INSERT ON system.points_checkout_coupons TO 'ddyidc_runtime'@'127.0.0.1';