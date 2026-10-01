"""Routing transformation tests use temporary files and mock all system commands."""
import runpy,sys,tempfile,unittest
from pathlib import Path
from unittest.mock import patch
from subprocess import CompletedProcess
fixture='''upstream ddyidc_backend { server 127.0.0.1:8080; }
server {
    server_name console.ddoidc.com;
    location /assets/ { try_files $uri =404; }
    location /admin/v1/ { proxy_pass http://ddyidc_backend; }
}
server {
    server_name cloud.ddoidc.com;
    ssl_certificate /existing/tls.pem;
    location /assets/ { try_files $uri =404; }
    location = / { try_files /public.html =503; }
    location = /products { try_files /public.html =503; }
    location ~ ^/products/[0-9]+/?$ { try_files /public.html =503; }
    location = /public.html { try_files $uri =503; }
    location / { return 404; }
}
'''
class Routing(unittest.TestCase):
    def run_script(self,path,code=0):
        with patch.object(sys,'argv',['portal-nginx.py','--config',str(path)]),patch('subprocess.run',return_value=CompletedProcess([],code,'','')):
            runpy.run_path(str(Path(__file__).with_name('portal-nginx.py')),run_name='__main__')
    def test_transform_preserves_console_tls_and_idempotence(self):
        with tempfile.TemporaryDirectory() as tmp:
            p=Path(tmp)/'nginx';p.write_text(fixture);self.run_script(p);s=p.read_text()
            self.assertIn('portal/document?path=$uri',s);self.assertIn('/existing/tls.pem',s);self.assertIn('location /admin/v1/',s)
            self.assertEqual(s.count('location ~ ^/portal-media/'),2)
            with self.assertRaises(SystemExit) as e:self.run_script(p)
            self.assertEqual(e.exception.code,0);self.assertEqual(p.read_text(),s)
    def test_validation_failure_restores_original(self):
        with tempfile.TemporaryDirectory() as tmp:
            p=Path(tmp)/'nginx';p.write_text(fixture)
            with self.assertRaises(SystemExit):self.run_script(p,1)
            self.assertEqual(p.read_text(),fixture)
    def test_unrecognized_config_not_changed(self):
        with tempfile.TemporaryDirectory() as tmp:
            p=Path(tmp)/'nginx';original=fixture.replace('location / { return 404; }','location / { deny all; }');p.write_text(original)
            with self.assertRaises(SystemExit):self.run_script(p)
            self.assertEqual(p.read_text(),original)
if __name__=='__main__':unittest.main()
