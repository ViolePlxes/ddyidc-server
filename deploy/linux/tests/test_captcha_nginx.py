import importlib.util
from pathlib import Path
import unittest

spec=importlib.util.spec_from_file_location('captcha_nginx',Path(__file__).resolve().parents[1]/'captcha-nginx.py')
module=importlib.util.module_from_spec(spec);spec.loader.exec_module(module)

class CSPTests(unittest.TestCase):
    def test_preserves_security_and_is_idempotent(self):
        old='add_header Content-Security-Policy "default-src \'self\'; script-src \'self\'; connect-src \'self\' wss:; frame-ancestors \'none\'; object-src \'none\'" always;'
        new=module.transform(old)
        self.assertEqual(module.transform(new),new)
        for required in ["frame-ancestors 'none'", "object-src 'none'", "wss:", "https://static.geetest.com"]: self.assertIn(required,new)
        self.assertNotIn('unsafe-eval',new);self.assertNotIn('unsafe-inline',new);self.assertNotIn('https://*',new)
    def test_missing_header_refused(self):
        with self.assertRaises(ValueError): module.transform('server {}')
    def test_html_cache_header_does_not_remove_csp(self):
        old='server {\n    add_header Content-Security-Policy "default-src \'self\'; script-src \'self\'" always;\n    location = /index.html { add_header Cache-Control "no-store"; }\n    location ~ "^/dd-admin-[a-z0-9]{24,64}(?:/|$)" {\n        add_header Set-Cookie $cookie always;\n        alias /dist/admin.html;\n    }\n}'
        new=module.transform(old)
        self.assertEqual(new.count('add_header Content-Security-Policy'),3)
        self.assertEqual(module.transform(new),new)

if __name__=='__main__': unittest.main()
