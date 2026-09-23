-- Production opt-in only. Do NOT source this script when balance_guard=off.
-- Stop all writers, complete migration and reconciliation, then install as DBA.
-- Application must not hold CREATE TRIGGER, DROP, ALTER, or audit DELETE privileges.
-- Only explicitly registered DBA identities can use the emergency session flag.
-- Do not grant the application INSERT/UPDATE/DELETE on this allowlist or audit.
CREATE TABLE IF NOT EXISTS billing.balance_guard_operators (
 db_user VARCHAR(128) PRIMARY KEY
);
INSERT IGNORE INTO billing.balance_guard_operators(db_user)
VALUES(SUBSTRING_INDEX(USER(),'@',1));
CREATE TABLE IF NOT EXISTS billing.balance_guard_audit (
 id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
 account_id BIGINT UNSIGNED NOT NULL,
 db_account VARCHAR(288) NOT NULL,
 source_ip VARCHAR(255) NOT NULL,
 reason VARCHAR(1000) NOT NULL,
 before_values JSON NOT NULL,
 after_values JSON NOT NULL,
 created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
DELIMITER $$
DROP TRIGGER IF EXISTS billing.balance_guard_update$$
CREATE TRIGGER billing.balance_guard_update BEFORE UPDATE ON billing.accounts FOR EACH ROW
BEGIN
 DECLARE matching INT DEFAULT 0;
 IF COALESCE(TRIM(@balance_admin_reason),'')<>'' THEN
  IF NOT EXISTS (SELECT 1 FROM billing.balance_guard_operators WHERE db_user=SUBSTRING_INDEX(USER(),'@',1)) THEN
   SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='DBA exception is not permitted for this database identity';
  END IF;
  INSERT INTO billing.balance_guard_audit(account_id,db_account,source_ip,reason,before_values,after_values)
  VALUES(OLD.id,USER(),SUBSTRING_INDEX(USER(),'@',-1),@balance_admin_reason,
   JSON_OBJECT('balance',OLD.balance,'frozen',OLD.frozen_balance,'recharge',OLD.total_recharge,'consume',OLD.total_consume,'refund',OLD.total_refund,'withdraw',OLD.total_withdraw,'version',OLD.version),
   JSON_OBJECT('balance',NEW.balance,'frozen',NEW.frozen_balance,'recharge',NEW.total_recharge,'consume',NEW.total_consume,'refund',NEW.total_refund,'withdraw',NEW.total_withdraw,'version',NEW.version));
 ELSE
  SELECT COUNT(*) INTO matching FROM billing.transactions t WHERE t.account_id=OLD.id
   AND t.customer_id=OLD.customer_id AND t.account_version=NEW.version
   AND t.balance_before=OLD.balance AND t.balance_after=NEW.balance
   AND t.frozen_before=OLD.frozen_balance AND t.frozen_after=NEW.frozen_balance
   AND t.amount=NEW.balance-OLD.balance
   AND NEW.credit_limit=OLD.credit_limit
   AND NEW.total_recharge=OLD.total_recharge+IF(t.type='recharge',t.amount,0)
   AND NEW.total_refund=OLD.total_refund+IF(t.type='refund',t.amount,0)
   AND NEW.total_withdraw=OLD.total_withdraw-IF(t.type='withdraw',t.amount,0)
   AND NEW.total_consume=CASE
     WHEN t.type IN ('order_pay','instance_renewal_pay') THEN OLD.total_consume-t.amount
     WHEN t.type='refund' AND t.channel='groupbuy' THEN GREATEST(0,OLD.total_consume-t.amount)
     ELSE OLD.total_consume END;
  IF matching<>1 OR NEW.version<>OLD.version+1 OR NEW.customer_id<>OLD.customer_id OR NEW.id<>OLD.id THEN
   SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='BalanceService ledger required; DBA must supply balance_admin_reason';
  END IF;
 END IF;
END$$
DROP TRIGGER IF EXISTS billing.balance_guard_insert$$
CREATE TRIGGER billing.balance_guard_insert BEFORE INSERT ON billing.accounts FOR EACH ROW
BEGIN
 IF NEW.balance<>0 OR NEW.frozen_balance<>0 OR NEW.total_recharge<>0 OR NEW.total_consume<>0 OR NEW.total_refund<>0 OR NEW.total_withdraw<>0 OR NEW.version<>0 THEN
  SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='New accounts must start empty; use BalanceService';
 END IF;
END$$
DROP TRIGGER IF EXISTS billing.balance_guard_delete$$
CREATE TRIGGER billing.balance_guard_delete BEFORE DELETE ON billing.accounts FOR EACH ROW
BEGIN
 SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='Balance accounts cannot be deleted';
END$$
DROP TRIGGER IF EXISTS billing.transactions_no_update$$
CREATE TRIGGER billing.transactions_no_update BEFORE UPDATE ON billing.transactions FOR EACH ROW
BEGIN
 SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='Balance ledger is append-only';
END$$
DROP TRIGGER IF EXISTS billing.transactions_no_delete$$
CREATE TRIGGER billing.transactions_no_delete BEFORE DELETE ON billing.transactions FOR EACH ROW
BEGIN
 SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='Balance ledger is append-only';
END$$
DELIMITER ;
-- DBA exception: SET @balance_admin_reason='ticket / correction reason';
-- Run the documented correction and SET @balance_admin_reason=NULL afterwards.
-- Exceptions are audited, never silently balanced by synthetic ledger entries.
