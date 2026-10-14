import importlib.util,pathlib,re,unittest
root=pathlib.Path(__file__).resolve().parents[1]
spec=importlib.util.spec_from_file_location('ledger_upgrade',root/'ledger-upgrade.py');module=importlib.util.module_from_spec(spec);spec.loader.exec_module(module)
class LedgerGuardUpgrade(unittest.TestCase):
 def test_types_and_protection(self):
  sql=(root.parents[1]/'sql/20260913_balance_guard.sql').read_text()
  body=re.search(r'CREATE TRIGGER billing.balance_guard_update.*?FOR EACH ROW\s*(BEGIN.*?END)\$\$',sql,re.S).group(1)
  old=body.replace("t.type IN ('order_pay','instance_renewal_pay','instance_upgrade_pay','promo_coupon_pay')","t.type IN ('order_pay','instance_renewal_pay')").replace("IF(t.type IN ('refund','promo_coupon_refund'),t.amount,0)","IF(t.type='refund',t.amount,0)")
  self.assertEqual(module.extend_guard(old),body);self.assertEqual(module.extend_guard(body),body)
  with self.assertRaises(RuntimeError):module.extend_guard('BEGIN SELECT 1; END')
  self.assertIn('DEFINER=`root`@`localhost`',module.create_guard(body,'root@localhost'))
  with self.assertRaises(RuntimeError):module.create_guard(body,'root@localhost;DROP')
if __name__=='__main__':unittest.main()
