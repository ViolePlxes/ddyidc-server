"""Real Nginx/TLS protocol regression with an isolated HTTP probe backend.

This checks proxy selection and redacted logging, not a live supplier VNC screen.
Run on the VM: python3 deploy/linux/tests/test_nginx_proxy.py
"""
import http.client
import http.server
import pathlib
import shutil
import socket
import ssl
import subprocess
import tempfile
import threading
import time
import unittest


def free_port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


class Probe(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path.split("?")[0] == "/api/v1/notifications/events":
            self.send_response(200)
            self.send_header("Content-Type", "text/event-stream")
            self.send_header("Cache-Control", "no-cache, no-store")
            self.end_headers()
            self.wfile.write(b"event: probe\ndata: first\n\n")
            self.wfile.flush()
            time.sleep(2)
            self.wfile.write(b"event: probe\ndata: second\n\n")
            self.wfile.flush()
            return
        if self.path.split("?")[0].endswith("/vnc/ws"):
            if self.headers.get("Upgrade", "").lower() != "websocket":
                self.send_error(400)
                return
            self.send_response(101)
            self.send_header("Upgrade", "websocket")
            self.send_header("Connection", "Upgrade")
        else:
            self.send_response(204)
        self.end_headers()

    def log_message(self, *_):
        pass


class NginxProxyTest(unittest.TestCase):
    def test_tls_routes_and_no_query_credentials_in_log(self):
        nginx = shutil.which("nginx")
        if not nginx:
            self.skipTest("nginx required")
        backend = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Probe)
        thread = threading.Thread(target=backend.serve_forever, daemon=True)
        thread.start()
        self.addCleanup(backend.server_close)
        self.addCleanup(backend.shutdown)
        with tempfile.TemporaryDirectory(prefix="ddyidc-nginx-test-") as folder:
            root = pathlib.Path(folder)
            cert, key = root / "cert.pem", root / "key.pem"
            subprocess.run(["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "1",
                            "-subj", "/CN=localhost", "-keyout", str(key), "-out", str(cert)],
                           check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            tls_port, http_port, health_port = free_port(), free_port(), free_port()
            template = pathlib.Path(__file__).resolve().parents[1] / "nginx-ddyidc.conf"
            body = template.read_text()
            body = body.replace("server 127.0.0.1:8080;", f"server 127.0.0.1:{backend.server_port};")
            body = body.replace("listen 80;", f"listen 127.0.0.1:{http_port};").replace("listen [::]:80;", "")
            body = body.replace("listen 443 ssl http2;", f"listen 127.0.0.1:{tls_port} ssl;").replace("listen [::]:443 ssl http2;", "")
            body = body.replace("listen 127.0.0.1:8081;", f"listen 127.0.0.1:{health_port};")
            body = body.replace("REPLACE_WITH_DOMAIN", "localhost")
            body = body.replace("/etc/ddyidc/tls/fullchain.pem", str(cert)).replace("/etc/ddyidc/tls/privkey.pem", str(key))
            body = body.replace("/var/log/nginx/ddyidc-access.log", str(root / "access.log"))
            body = body.replace("/var/log/nginx/ddyidc-error.log", str(root / "error.log"))
            config = root / "nginx.conf"
            config.write_text(f"pid {root}/nginx.pid; error_log {root}/main.log; events {{}} http {{ access_log off; "
                              f"client_body_temp_path {root}/body; proxy_temp_path {root}/proxy; "
                              f"fastcgi_temp_path {root}/fastcgi; uwsgi_temp_path {root}/uwsgi; "
                              f"scgi_temp_path {root}/scgi; {body} }}")
            syntax = subprocess.run([nginx, "-p", folder, "-c", str(config), "-t"], capture_output=True, text=True)
            self.assertEqual(syntax.returncode, 0, syntax.stderr)
            process = subprocess.Popen([nginx, "-p", folder, "-c", str(config), "-g", "daemon off;"],
                                       stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            try:
                deadline = time.monotonic() + 5
                while True:
                    try:
                        with socket.create_connection(("127.0.0.1", tls_port), timeout=0.2):
                            break
                    except OSError:
                        if time.monotonic() > deadline:
                            self.fail("isolated nginx did not start")
                        time.sleep(0.02)
                paths = ["/api/v1/instance/42/vnc/ws", "/admin/v1/customers/7/instances/42/vnc/ws"]
                for path in paths:
                    conn = http.client.HTTPSConnection("127.0.0.1", tls_port, context=ssl._create_unverified_context(), timeout=3)
                    conn.request("GET", path + "?token=TEST_ONLY_DO_NOT_LOG", headers={"Upgrade": "websocket", "Connection": "Upgrade"})
                    self.assertEqual(conn.getresponse().status, 101, path)
                    conn.close()
                for path in ["/api/v1/products", "/admin/v1/customers"]:
                    conn = http.client.HTTPSConnection("127.0.0.1", tls_port, context=ssl._create_unverified_context(), timeout=3)
                    conn.request("GET", path)
                    self.assertEqual(conn.getresponse().status, 204, path)
                    conn.close()
                conn = http.client.HTTPSConnection("127.0.0.1", tls_port, context=ssl._create_unverified_context(), timeout=4)
                started = time.monotonic()
                conn.request("GET", "/api/v1/notifications/events?cursor=TEST_ONLY_DO_NOT_LOG", headers={"Accept": "text/event-stream"})
                stream = conn.getresponse()
                self.assertEqual(stream.status, 200)
                self.assertEqual(stream.readline(), b"event: probe\n")
                self.assertEqual(stream.readline(), b"data: first\n")
                self.assertLess(time.monotonic()-started, 1.0, "SSE was buffered until upstream completed")
                self.assertEqual(stream.readline(), b"\n")
                self.assertEqual(stream.readline(), b"event: probe\n")
                self.assertEqual(stream.readline(), b"data: second\n")
                conn.close()
            finally:
                process.terminate()
                process.wait(timeout=5)
            log = (root / "access.log").read_text()
            self.assertNotIn("TEST_ONLY_DO_NOT_LOG", log)
            for path in paths:
                self.assertIn(path, log)


if __name__ == "__main__":
    unittest.main()
