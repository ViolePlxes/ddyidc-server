import importlib.util,unittest
from pathlib import Path
s=importlib.util.spec_from_file_location('egress',Path(__file__).resolve().parents[1]/'captcha-egress.py');m=importlib.util.module_from_spec(s);s.loader.exec_module(m)
class Tests(unittest.TestCase):
 def test_no_private_or_empty_destinations(self):
  for rows in [[],[(2,1,6,'',('127.0.0.1',443))],[(2,1,6,'',('10.0.0.1',443))]]:
   with self.assertRaises(ValueError):m.public_networks(rows)
 def test_preserves_other_rules_and_excludes_transient_overlay(self):
  source='# /etc/systemd/system/ddyidc.service\n[Service]\nIPAddressAllow=localhost\n# /etc/systemd/system/ddyidc.service.d/oss.conf\n[Service]\nIPAddressAllow=59.110.117.233/32\n# /run/systemd/system.control/ddyidc.service.d/50.conf\n[Service]\nIPAddressAllow=1.2.3.4/32\n'
  result=m.persistent_allow(source)
  self.assertIn('59.110.117.233/32',result);self.assertIn('127.0.0.0/8 ::1/128',result);self.assertNotIn('1.2.3.4/32',result)
if __name__=='__main__':unittest.main()
