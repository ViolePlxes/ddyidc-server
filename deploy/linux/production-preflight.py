#!/usr/bin/env python3
"""Read-only production gate. Reports check names, never configuration values.

Run locally as root after migration, before opening production traffic.
This checks configuration, not public callback reachability or real settlement.
"""
import argparse
import ipaddress
import json
import os
from pathlib import Path
import subprocess
import sys
from urllib.parse import urlsplit
import yaml


KEYS = (
    'security.cookie_secure', 'security.csrf_enabled', 'payment.mock_enabled',
    'payment.wechat.enabled', 'payment.wechat.base_url', 'payment.alipay.enabled',
    'payment.alipay.gateway', 'payment.alipay.notify_url',
    'payment.alipay.return_url', 'payment.alipay.quit_url',
)


def enabled(value):
    return str(value).lower() in ('true', '1')


def public_https(value):
    try:
        u = urlsplit(str(value))
        host = (u.hostname or '').lower().rstrip('.')
        if u.scheme != 'https' or not host or u.username or u.password or u.fragment or u.port not in (None, 443):
            return False
        if host == 'localhost' or host.endswith(('.localhost', '.local', '.test', '.invalid', '.example', '.example.com', '.example.org', '.example.net')) or host in ('example.com', 'example.org', 'example.net'):
            return False
        try:
            return ipaddress.ip_address(host).is_global
        except ValueError:
            return '.' in host and all(label and not label.startswith('-') and not label.endswith('-') and all(c.isascii() and (c.isalnum() or c == '-') for c in label) for label in host.split('.'))
    except (ValueError, TypeError):
        return False


def check_config(config, values):
    checks = []
    def add(name, passed): checks.append({'check': name, 'passed': bool(passed)})
    add('production environment', config.get('DeploymentEnvironment') == 'production' and config.get('Mode') == 'pro')
    add('loopback API listener', config.get('Host') in ('127.0.0.1', '::1'))
    add('dedicated database identity', config.get('MySQL', {}).get('User') == 'ddyidc_runtime')
    policy = config.get('UpstreamTestPolicy') or {}
    add('no supplier test overrides', not enabled(policy.get('Enabled')) and not policy.get('Rules'))
    add('real notification mode', not enabled((config.get('Notify') or {}).get('Mock')))
    add('real payment mode', str(values.get('payment.mock_enabled', '')).lower() in ('false', '0'))
    add('secure session cookies', enabled(values.get('security.cookie_secure')))
    add('CSRF enabled', enabled(values.get('security.csrf_enabled')))
    proxies = (config.get('Network') or {}).get('TrustedProxies') or []
    try:
        trusted = bool(proxies) and all(ipaddress.ip_network(x, strict=False).prefixlen > 0 for x in proxies)
    except (ValueError, TypeError): trusted = False
    add('explicit non-wildcard trusted proxies', trusted)
    for section, key, length in [('Crypto', 'AESKey', 32), ('JWT', 'AccessSecret', 32), ('JWT', 'RefreshSecret', 32), ('MySQL', 'Password', 16), ('Redis', 'Password', 16)]:
        value = str((config.get(section) or {}).get(key, ''))
        add(section + '.' + key + ' configured', len(value) >= length and '__' not in value and 'changeme' not in value.lower() and 'example' not in value.lower())
    jwt = config.get('JWT') or {}
    add('distinct token keys', jwt.get('AccessSecret') != jwt.get('RefreshSecret'))
    if enabled(values.get('payment.wechat.enabled')):
        add('WeChat actual callback base HTTPS', public_https(values.get('payment.wechat.base_url')))
    if enabled(values.get('payment.alipay.enabled')):
        add('Alipay production gateway', values.get('payment.alipay.gateway', '').rstrip('/') == 'https://openapi.alipay.com/gateway.do')
        for key in ('notify_url', 'return_url', 'quit_url'):
            add('Alipay ' + key + ' public HTTPS', public_https(values.get('payment.alipay.' + key)))
    return checks


def mysql(query):
    r = subprocess.run(['mysql', '--protocol=socket', '-uroot', '-NB', '--raw'], input=query, text=True, capture_output=True, timeout=15)
    if r.returncode: raise RuntimeError('database checks unavailable')
    return r.stdout.strip()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--config', default='/etc/ddyidc/config.yaml')
    args = parser.parse_args()
    if os.geteuid() != 0: raise RuntimeError('local root read-only database inspection required')
    config = yaml.safe_load(Path(args.config).read_text())
    # Only non-secret keys are selected. No payment credential values are read.
    raw = mysql("SELECT `key`,value FROM system.system_configs WHERE `key` IN (" + ','.join("'" + k + "'" for k in KEYS) + ')')
    values = dict(line.split('\t', 1) for line in raw.splitlines() if '\t' in line)
    checks = check_config(config, values)
    add = lambda name, passed: checks.append({'check': name, 'passed': bool(passed)})
    service = subprocess.run(['systemctl', 'show', 'ddyidc', '--property=ExecStartPre', '--value'], capture_output=True, text=True, timeout=10)
    add('no local feature override service', service.returncode == 0 and 'localfeatures' not in service.stdout and not Path('/opt/ddyidc/bin/ddyidc-localfeatures').exists() and not Path('/etc/systemd/system/ddyidc-localfeatures.service').exists() and not Path('/opt/ddyidc/deploy/localfeatures.sh').exists())
    add('balance guard installed', mysql("SELECT COUNT(DISTINCT TRIGGER_NAME) FROM information_schema.TRIGGERS WHERE TRIGGER_SCHEMA='billing' AND TRIGGER_NAME IN ('balance_guard_update','balance_guard_insert','balance_guard_delete','transactions_no_update','transactions_no_delete')") == '5')
    dangerous = "('ALTER','CREATE','DROP','TRIGGER','SUPER','GRANT OPTION','FILE','CREATE USER')"
    principal = "GRANTEE LIKE '''ddyidc_runtime''@%'"
    for table in ('USER_PRIVILEGES', 'SCHEMA_PRIVILEGES', 'TABLE_PRIVILEGES'):
        add('runtime restricted ' + table.lower(), mysql('SELECT COUNT(*) FROM information_schema.' + table + ' WHERE ' + principal + ' AND (PRIVILEGE_TYPE IN ' + dangerous + " OR IS_GRANTABLE='YES')") == '0')
    add('no inherited runtime privilege roles', mysql("SELECT COUNT(*) FROM mysql.role_edges WHERE TO_USER='ddyidc_runtime'") == '0')
    add('no global write grants', mysql("SELECT COUNT(*) FROM information_schema.USER_PRIVILEGES WHERE " + principal + " AND PRIVILEGE_TYPE IN ('INSERT','UPDATE','DELETE')") == '0')
    add('no schema-wide financial mutation grants', mysql("SELECT COUNT(*) FROM information_schema.SCHEMA_PRIVILEGES WHERE " + principal + " AND TABLE_SCHEMA IN ('billing','payment') AND PRIVILEGE_TYPE IN ('UPDATE','DELETE')") == '0')
    add('immutable ledger grants', mysql("SELECT COUNT(*) FROM information_schema.TABLE_PRIVILEGES WHERE " + principal + " AND TABLE_SCHEMA='billing' AND TABLE_NAME='transactions' AND PRIVILEGE_TYPE IN ('UPDATE','DELETE')") == '0')
    passed = all(c['passed'] for c in checks)
    print(json.dumps({'passed': passed, 'checks': checks, 'boundary': 'Read-only configuration gate; still requires verify-linux.sh, public HTTPS callback, real channel credentials/settlement, credential rotation and test-account isolation evidence.'}, ensure_ascii=False, indent=2))
    return 0 if passed else 1


if __name__ == '__main__':
    try: sys.exit(main())
    except Exception:
        print(json.dumps({'passed': False, 'error': 'preflight inspection failed; check local permissions, config format and database availability; sensitive details withheld'}))
        sys.exit(2)
