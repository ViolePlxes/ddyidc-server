-- A verified provider identity has one owner even after switching OAuth apps.
-- Existing customer.oauth_accounts remains the compatibility/read model.
ALTER TABLE customer.social_bindings
  DROP INDEX social_identity,
  ADD UNIQUE KEY social_identity (provider,open_id);
