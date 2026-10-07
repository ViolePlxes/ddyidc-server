"""Pure transform plus real isolated Nginx entry checks, with a gate probe."""
import importlib.util
import pathlib
import tempfile
import unittest
import subprocess
import socket
import threading
import http.server
import http.client
import time
import shutil

root=pathlib.Path(__file__).resolve().parents[1]
spec=importlib.util.spec_from_file_location('admin_security',root/'admin-security-nginx.py')
module=importlib.util.module_from_spec(spec);spec.loader.exec_module(module)
correct='/dd-admin-'+'a'*24
def port():
 with socket.socket() as s:s.bind(('127.0.0.1',0));return s.getsockname()[1]
class Gate(http.server.BaseHTTPRequestHandler):
 def do_GET(self):
  value=self.headers.get('X-Original-URI','').split('?')[0]
  self.send_response(204 if value==correct or value.startswith(correct+'/') else 403)
  self.send_header('Set-Cookie','entry-probe=passed; HttpOnly; Path=/');self.end_headers()
 def log_message(self,*args):pass
class TestAdminSecurityNginx(unittest.TestCase):
 def test_portal_does_not_redirect_legacy_admin(self):
  portal='location ~ ^/(login|register|reset-password|forgot-password|console|admin|sales|cart|order|bind-mobile|auth|wallet)(?:/|$) { return 302 https://console.example$request_uri; }'
  installed='location = /_ddyidc_admin_entry_check {}\n'+portal
  transformed=module.transform(installed)
  self.assertNotIn('console|admin|sales',transformed)
  self.assertIn('console|sales|cart',transformed)
  self.assertEqual(module.transform(transformed),transformed)
 def test_transform_and_real_gate(self):
  source=(root/'nginx-ddyidc.conf').read_text()
  legacy=source.replace('location = /admin.html { internal;','location = /admin.html {')
  start=legacy.index('    location ~ ^/(?:admin|backend|manage)');stop=legacy.index('    location ~ ^/sales',start)
  legacy=legacy[:start]+'    location ~ ^/admin(?:/|$) { try_files $uri $uri/ /admin.html; }\n'+legacy[stop:]
  upgraded=module.transform(legacy)
  self.assertEqual(module.transform(upgraded),upgraded)
  self.assertIn('ssl_certificate /etc/ddyidc/tls/fullchain.pem',upgraded)
  self.assertIn('location ^~ /admin/v1/',upgraded)
  if not shutil.which('nginx'):self.fail('Nginx required in VM')
  server=http.server.ThreadingHTTPServer(('127.0.0.1',0),Gate);threading.Thread(target=server.serve_forever,daemon=True).start()
  with tempfile.TemporaryDirectory(prefix='ddyidc-mfa-nginx-') as folder:
   path=pathlib.Path(folder);(path/'admin.html').write_text('secure-admin-probe');(path/'index.html').write_text('portal-probe')
   routing=upgraded[upgraded.index('    location ~ ^/(?:admin|backend|manage)'):upgraded.index('    location ~ ^/sales')]
   routing=routing.replace('/var/lib/ddyidc/update/workspaces/frontend/dist/admin.html',str(path/'admin.html'))
   number=port();config=path/'nginx.conf';config.write_text(f'pid {path}/nginx.pid; error_log {path}/error.log; events {{}} http {{ access_log off; upstream ddyidc_backend {{ server 127.0.0.1:{server.server_port}; }} server {{ listen 127.0.0.1:{number}; root {path}; location = /admin.html {{ internal; }} '+routing+' location / { try_files $uri /index.html; } } }')
   check=subprocess.run(['nginx','-p',folder,'-c',str(config),'-t'],capture_output=True,text=True);self.assertEqual(check.returncode,0,check.stderr)
   process=subprocess.Popen(['nginx','-p',folder,'-c',str(config),'-g','daemon off;'],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
   try:
    time.sleep(.3)
    for route,want in [('/admin',404),('/admin/login',404),('/backend',404),('/manage',404),('/admin.html',404),('/dd-admin-'+'b'*24+'/login',404),(correct+'/login',200),(correct+'/todos/refunds',200),('/',200)]:
     client=http.client.HTTPConnection('127.0.0.1',number,timeout=5);client.request('GET',route);response=client.getresponse();body=response.read();self.assertEqual(response.status,want,route)
     if want==200 and route!='/':self.assertEqual(body,b'secure-admin-probe');self.assertIn('entry-probe',response.getheader('Set-Cookie',''));self.assertEqual(response.getheader('Content-Type'),'text/html')
     client.close()
   finally:process.terminate();process.wait(timeout=10);server.shutdown();server.server_close()
if __name__=='__main__':unittest.main()
