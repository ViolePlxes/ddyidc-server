"""Two live HTTP nodes behind real nginx; controlled RFB banner, not supplier GUI."""
import base64
import hashlib
import http.client
import http.server
import pathlib
import shutil
import socket
import subprocess
import tempfile
import threading
import time
import unittest


class Node(http.server.BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def do_GET(self):
        key = self.headers.get("Sec-WebSocket-Key", "")
        accept = base64.b64encode(hashlib.sha1((key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11").encode()).digest()).decode()
        self.send_response(101)
        self.send_header("Upgrade", "websocket")
        self.send_header("Connection", "Upgrade")
        self.send_header("Sec-WebSocket-Accept", accept)
        self.send_header("X-Test-Node", self.server.node)
        self.end_headers()
        self.wfile.write(b"\x82\x0cRFB 003.008\n")
        self.wfile.flush()
        self.close_connection = True

    def log_message(self, *_):
        pass


class NodesTest(unittest.TestCase):
    def test_node_routing_and_fail_closed(self):
        nginx = shutil.which("nginx")
        if not nginx:
            self.skipTest("nginx required")
        nodes = []
        for name in ("node-a", "node-b"):
            server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Node)
            server.node = name
            threading.Thread(target=server.serve_forever, daemon=True).start()
            nodes.append(server)
        with socket.socket() as probe:
            probe.bind(("127.0.0.1", 0))
            port = probe.getsockname()[1]
        with tempfile.TemporaryDirectory(prefix="ddyidc-vnc-nodes-") as folder:
            root = pathlib.Path(folder)
            template = pathlib.Path(__file__).resolve().parents[1] / "nginx-vnc-nodes.conf.example"
            maps = template.read_text().replace("127.0.0.1:8080", f"127.0.0.1:{nodes[0].server_port}").replace("127.0.0.1:8082", f"127.0.0.1:{nodes[1].server_port}")
            config = root / "nginx.conf"
            config.write_text(f"""
pid {root}/nginx.pid; error_log {root}/error.log;
events {{}}
http {{
  client_body_temp_path {root}/body; proxy_temp_path {root}/proxy;
  fastcgi_temp_path {root}/fastcgi; uwsgi_temp_path {root}/uwsgi; scgi_temp_path {root}/scgi;
  log_format safe '$uri $status'; access_log {root}/access.log safe;
  {maps}
  server {{
    listen 127.0.0.1:{port};
    location ~ ^/(api/v1/instance/[0-9]+|admin/v1/customers/[0-9]+/instances/[0-9]+)/vnc/ws$ {{
      if ($ddyidc_vnc_backend = "") {{ return 403; }}
      proxy_pass http://$ddyidc_vnc_backend;
      proxy_next_upstream off;
      proxy_http_version 1.1;
      proxy_set_header Upgrade $http_upgrade;
      proxy_set_header Connection upgrade;
      proxy_buffering off;
    }}
  }}
}}
""")
            syntax = subprocess.run([nginx, "-p", folder, "-c", str(config), "-t"], capture_output=True, text=True)
            self.assertEqual(syntax.returncode, 0, syntax.stderr)
            process = subprocess.Popen([nginx, "-p", folder, "-c", str(config), "-g", "daemon off;"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            paths = ("/api/v1/instance/42/vnc/ws", "/admin/v1/customers/7/instances/42/vnc/ws")
            try:
                for attempt in range(100):
                    try:
                        with socket.create_connection(("127.0.0.1", port), timeout=.1):
                            break
                    except OSError:
                        time.sleep(.05)
                for path in paths:
                    for node in ("node-a", "node-b"):
                        with socket.create_connection(("127.0.0.1", port), timeout=3) as sock:
                            request = f"GET {path}?token={node}.{'a'*64} HTTP/1.1\r\nHost: localhost\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n\r\n"
                            sock.sendall(request.encode())
                            data = b""
                            while b"RFB 003.008\n" not in data:
                                part = sock.recv(4096)
                                if not part:
                                    break
                                data += part
                            self.assertIn(b"101", data)
                            self.assertIn(f"X-Test-Node: {node}".encode(), data)
                            self.assertIn(b"\x82\x0cRFB 003.008\n", data)
                nodes[0].shutdown()
                nodes[0].server_close()
                for token, status in (("node-a." + "a"*64, 502), ("unknown." + "a"*64, 403), ("http://127.0.0.1", 403)):
                    conn = http.client.HTTPConnection("127.0.0.1", port, timeout=3)
                    conn.request("GET", paths[0] + "?token=" + token, headers={"Upgrade":"websocket", "Connection":"Upgrade"})
                    self.assertEqual(conn.getresponse().status, status)
                    conn.close()
            finally:
                process.terminate()
                process.wait(timeout=5)
                for server in nodes:
                    server.shutdown()
                    server.server_close()
            self.assertNotIn("a"*64, (root / "access.log").read_text())


if __name__ == "__main__":
    unittest.main()
