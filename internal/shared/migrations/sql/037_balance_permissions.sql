INSERT INTO system.permissions(id,code,name,module) VALUES
 (800000000003701,'balance:adjust','人工调整余额','balance'),
 (800000000003702,'balance:recharge','人工确认充值','balance'),
 (800000000003703,'balance:freeze','冻结余额','balance'),
 (800000000003704,'balance:unfreeze','解冻余额','balance')
 ON DUPLICATE KEY UPDATE id=id;
