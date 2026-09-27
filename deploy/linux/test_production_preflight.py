import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location('preflight', Path(__file__).with_name('production-preflight.py'))
gate = importlib.util.module_from_spec(spec)
spec.loader.exec_module(gate)


class ProductionGateTests(unittest.TestCase):
    def test_valid_synthetic_configuration_passes_static_checks(self):
        config = {'DeploymentEnvironment': 'production', 'Mode': 'pro', 'Host': '127.0.0.1', 'MySQL': {'User': 'ddyidc_runtime', 'Password': 'm' * 32}, 'Redis': {'Password': 'r' * 32}, 'Crypto': {'AESKey': 'a' * 32}, 'JWT': {'AccessSecret': 'j' * 32, 'RefreshSecret': 'k' * 32}, 'Network': {'TrustedProxies': ['127.0.0.1/32']}, 'Notify': {'Mock': False}}
        values = {'payment.mock_enabled': 'false', 'security.cookie_secure': 'true', 'security.csrf_enabled': 'true', 'payment.wechat.enabled': '1', 'payment.wechat.base_url': 'https://pay.company.cn', 'payment.alipay.enabled': '1', 'payment.alipay.gateway': 'https://openapi.alipay.com/gateway.do'}
        for key in ('notify_url', 'return_url', 'quit_url'): values['payment.alipay.' + key] = 'https://pay.company.cn/' + key
        self.assertTrue(all(x['passed'] for x in gate.check_config(config, values)))

    def test_rejects_local_and_disguised_callback_urls(self):
        for value in ('http://pay.company.cn', 'https://127.0.0.1', 'https://[::1]', 'https://10.2.3.4', 'https://localhost.', 'https://foo.example.com', 'https://user:secret@company.cn', 'https://company.cn:8080', 'https://company.cn/#fragment', 'https://company.cn:invalid'):
            with self.subTest(value=value): self.assertFalse(gate.public_https(value))

    def test_public_https_syntax_is_not_connectivity_evidence(self):
        self.assertTrue(gate.public_https('https://pay.company.cn/api/notify'))

    def test_local_vm_is_not_production_ready(self):
        checks = gate.check_config({'DeploymentEnvironment': 'local-vm', 'Host': '0.0.0.0', 'Notify': {'Mock': True}}, {})
        failed = {x['check'] for x in checks if not x['passed']}
        self.assertIn('production environment', failed)
        self.assertIn('secure session cookies', failed)
        self.assertIn('real notification mode', failed)
        self.assertIn('loopback API listener', failed)

    def test_uses_wechat_base_not_unused_notify_setting(self):
        checks = gate.check_config({}, {'payment.wechat.enabled': '1', 'payment.wechat.base_url': 'http://127.0.0.1', 'payment.wechat.notify_url': 'https://pay.company.cn/notify'})
        self.assertFalse(next(c['passed'] for c in checks if c['check'] == 'WeChat actual callback base HTTPS'))

    def test_output_never_contains_values(self):
        private = 'sensitive-sentinel-only-in-test'
        checks = gate.check_config({'JWT': {'AccessSecret': private}}, {'payment.alipay.enabled': 'true', 'payment.alipay.notify_url': private})
        self.assertNotIn(private, str(checks))

    def test_rejects_wildcard_proxy_and_template_keys(self):
        checks = gate.check_config({'Network': {'TrustedProxies': ['0.0.0.0/0']}, 'JWT': {'AccessSecret': '__JWT_ACCESS_SECRET__'}}, {})
        self.assertFalse(next(c['passed'] for c in checks if c['check'] == 'explicit non-wildcard trusted proxies'))
        self.assertFalse(next(c['passed'] for c in checks if c['check'] == 'JWT.AccessSecret configured'))


if __name__ == '__main__': unittest.main()
