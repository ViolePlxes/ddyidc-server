#!/usr/bin/env python3
"""Install dynamic admin entry checks, preserving existing TLS and API routing."""
import argparse
import re
import subprocess
from pathlib import Path

def transform(source: str) -> str:
    # The separate public portal must not redirect a scanned /admin path to
    # Console. Its existing fallback returns 404 after removing that alias.
    source = source.replace('forgot-password|console|admin|sales|cart',
                            'forgot-password|console|sales|cart')
    if 'location = /_ddyidc_admin_entry_check' in source:
        return source
    old = re.search(r'location\s+~\s+\^/admin\(\?:/\|\$\)\s*\{', source)
    if not old:
        raise ValueError('Existing admin SPA location not found; preserve configuration for review')
    end = old.end(); depth = 1
    while depth and end < len(source):
        if source[end] == '{': depth += 1
        if source[end] == '}': depth -= 1
        end += 1
    if depth: raise ValueError('Admin location boundary invalid')
    template = Path(__file__).with_name('nginx-ddyidc.conf').read_text()
    start = template.index('    location ~ ^/(?:admin|backend|manage)')
    stop = template.index('    location ~ ^/sales', start)
    block = template[start:stop].strip()
    source = source[:old.start()] + block + source[end:]
    source, count = re.subn(r'location\s*=\s*/admin\.html\s*\{', 'location = /admin.html { internal;', source)
    if count != 1: raise ValueError('Unique admin HTML location required')
    return source

def main():
    parser=argparse.ArgumentParser();parser.add_argument('--config',default='/etc/nginx/sites-available/ddyidc');args=parser.parse_args()
    path=Path(args.config);old=path.read_text();new=transform(old)
    if old==new: print('Admin security routing already installed');return
    backup=path.with_name(path.name+'.before-admin-security')
    if not backup.exists():backup.write_text(old)
    path.write_text(new)
    result=subprocess.run(['nginx','-t'],capture_output=True,text=True)
    if result.returncode:
        path.write_text(old);raise SystemExit('Nginx syntax check failed; original restored')
    try:subprocess.run(['systemctl','reload','nginx'],check=True)
    except Exception:
        path.write_text(old);subprocess.run(['systemctl','reload','nginx'],check=False);raise
    print('Dynamic admin entry checks installed; existing TLS and business APIs preserved')

if __name__=='__main__':main()
