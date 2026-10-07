#!/usr/bin/env python3
"""Add narrowly scoped GeeTest v4 origins; preserve all other CSP directives."""
import argparse
import re
import subprocess
from pathlib import Path

def transform(source):
    def update(match):
        policy = match.group(2)
        directives = [part.strip() for part in policy.split(';') if part.strip()]
        additions = {
            'script-src': ["'self'", 'https://static.geetest.com', 'https://static.geevisit.com', 'https://gcaptcha4.geetest.com', 'https://gcaptcha4.geevisit.com', 'https://gcaptcha4.gsensebot.com'],
            'style-src': ["'self'", 'https://static.geetest.com', 'https://static.geevisit.com'],
            'connect-src': ["'self'", 'https://gcaptcha4.geetest.com', 'https://gcaptcha4.geevisit.com', 'https://gcaptcha4.gsensebot.com', 'https://static.geetest.com', 'https://static.geevisit.com'],
            'frame-src': ["'self'", 'https://gcaptcha4.geetest.com', 'https://gcaptcha4.geevisit.com', 'https://gcaptcha4.gsensebot.com'],
        }
        for name, origins in additions.items():
            index = next((i for i, d in enumerate(directives) if d.split()[0] == name), None)
            parts = directives[index].split() if index is not None else [name]
            for origin in origins:
                if origin not in parts: parts.append(origin)
            if index is None: directives.append(' '.join(parts))
            else: directives[index] = ' '.join(parts)
        return match.group(1) + '; '.join(directives) + match.group(3)
    result, count = re.subn(r'(add_header\s+Content-Security-Policy\s+")([^"]*)("\s+always;)', update, source)
    if count < 1: raise ValueError('Explicit CSP header not found; automatic modification refused')
    # Nginx does not inherit server add_header when a location declares any
    # add_header. Preserve CSP on HTML locations that set caching/entry cookies.
    header = re.search(r'add_header\s+Content-Security-Policy\s+"[^"]*"\s+always;', result).group(0)
    starts = list(re.finditer(r'(?m)^\s*location[^\n]*\s\{', result))
    for match in reversed(starts):
        declaration = match.group(0)
        if not any(name in declaration for name in ['/index.html', '/console.html', '/admin.html', '/sales.html', '/dd-admin-']): continue
        end = result.find('}', match.end())
        if end < 0: raise ValueError('HTML location boundary not found')
        block = result[match.end():end]
        if 'add_header' in block and 'Content-Security-Policy' not in block:
            result = result[:match.end()] + '\n        ' + header + result[match.end():]
    return result

def main():
    parser = argparse.ArgumentParser(); parser.add_argument('--config', default='/etc/nginx/sites-available/ddyidc'); args = parser.parse_args()
    path = Path(args.config); old = path.read_text(); new = transform(old)
    if old == new: print('Captcha CSP already installed'); return
    backup = path.with_name(path.name + '.before-captcha')
    if not backup.exists(): backup.write_text(old)
    path.write_text(new)
    if subprocess.run(['nginx', '-t'], capture_output=True).returncode:
        path.write_text(old); raise SystemExit('Nginx check failed; original restored')
    try: subprocess.run(['systemctl', 'reload', 'nginx'], check=True)
    except Exception:
        path.write_text(old); subprocess.run(['systemctl', 'reload', 'nginx'], check=False); raise
    print('Captcha CSP installed')

if __name__ == '__main__': main()
