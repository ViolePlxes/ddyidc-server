ALTER TABLE system.points_settings ADD COLUMN daily_reward_limit BIGINT NOT NULL DEFAULT 20, ADD COLUMN invite_monthly_limit BIGINT NOT NULL DEFAULT 10000, ADD COLUMN checkout_daily_limit INT NOT NULL DEFAULT 1;
CREATE INDEX idx_points_order_daily ON system.points_orders (user_id,goods_id,created_at,status);
ALTER TABLE system.points_checkout_coupons ADD COLUMN created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP;
UPDATE system.points_checkout_coupons pc JOIN marketing.user_coupons uc ON uc.id=pc.user_coupon_id SET pc.created_at=uc.created_at;
CREATE INDEX idx_points_checkout_daily ON system.points_checkout_coupons (user_id,created_at);
