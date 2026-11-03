#!/usr/bin/env python3
"""Adapt the existing dual-domain Nginx configuration without replacing TLS or APIs."""
import argparse,subprocess
from pathlib import Path
parser=argparse.ArgumentParser();parser.add_argument('--config',default='/etc/nginx/sites-available/ddyidc');args=parser.parse_args()
path=Path(args.config);source=path.read_text();marker='server_name cloud.ddoidc.com;'
if marker not in source:raise SystemExit('independent public domain block required')
start=source.rfind('server {',0,source.index(marker));end=source.find('\n}',source.index(marker))+2
if start<0 or end<2:raise SystemExit('public block boundary unavailable')
block=source[start:end]
proxy='''
        limit_except GET { deny all; }
        proxy_pass http://ddyidc_backend/api/v1/portal/document?path=$uri;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
'''
if 'portal/document?path=$uri' not in block:
    expected=['location = / { try_files /public.html =503; }','location = /products { try_files /public.html =503; }','location ~ ^/products/[0-9]+/?$ { try_files /public.html =503; }']
    if 'location / { return 404; }' not in block or '    location /assets/' not in block:
        raise SystemExit('public fallback or asset routing differs; preserve config for review')
    for item in expected:
        if item not in block:raise SystemExit('public routing differs; preserve config for review')
        block=block.replace(item,'')
    block=block.replace('location = /public.html { try_files $uri =503; }','location = /public.html { return 301 /; }')
    block=block.replace('location / { return 404; }','location / {'+proxy+'    }')
    reads='''
    location = /api/v1/portal/revision { limit_except GET { deny all; } proxy_pass http://ddyidc_backend; }
    location = /api/v1/portal/site { limit_except GET { deny all; } proxy_pass http://ddyidc_backend; }
    location = /api/v1/portal/pages { limit_except GET { deny all; } proxy_pass http://ddyidc_backend; }
'''
    block=block.replace('    location /assets/',reads+'    location /assets/')
    source=source[:start]+block+source[end:]
if 'location = /api/v1/portal/content ' not in source:
    pos=source.index(marker)
    slot=source.find('\n',pos)+1
    source=source[:slot]+'    location = /api/v1/portal/content { limit_except GET { deny all; } proxy_pass http://ddyidc_backend; }\n'+source[slot:]
media='''    location ~ ^/portal-media/([a-f0-9-]+)$ {
        limit_except GET { deny all; }
        proxy_pass http://ddyidc_backend/api/v1/portal/media/$1;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
'''
if 'location ~ ^/portal-media/' not in source:source=source.replace('    location /assets/',media+'    location /assets/')
if source==path.read_text():print('Portal Nginx routing already applied.');raise SystemExit(0)
backup=path.with_name(path.name+'.before-portal')
if not backup.exists():backup.write_text(path.read_text())
old=path.read_text();path.write_text(source)
result=subprocess.run(['nginx','-t'],capture_output=True,text=True)
if result.returncode:path.write_text(old);raise SystemExit('Nginx validation failed; original configuration restored')
subprocess.run(['systemctl','reload','nginx'],check=True)
print('Portal static routing installed; existing Console, TLS and business routes preserved.')
