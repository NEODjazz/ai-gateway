"""Exercise the real Node HTTPS fixture without Docker or external services."""

import http.server
import importlib.util
import json
import os
from pathlib import Path
import selectors
import shutil
import socket
import ssl
import subprocess
import tempfile
import threading
import unittest
import urllib.error
import urllib.request
from unittest.mock import patch


ROOT = Path(__file__).resolve().parent.parent
spec = importlib.util.spec_from_file_location("identity_https_integration", Path(__file__).with_name("test-identity-integration.py"))
identity = importlib.util.module_from_spec(spec)
spec.loader.exec_module(identity)


class Upstream(http.server.BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def do_GET(self):
        body = json.dumps({
            "path": self.path,
            "proto": self.headers.get("X-Forwarded-Proto"),
        }).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)


class TLSProxyTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        for command in ("node", "openssl"):
            if shutil.which(command) is None:
                raise RuntimeError(command + " is required for the HTTPS fixture tests")

    def setUp(self):
        directory = tempfile.TemporaryDirectory(prefix="gateway-identity-tls-")
        self.addCleanup(directory.cleanup)
        self.directory = Path(directory.name)
        os.chmod(self.directory, 0o700)
        config = self.directory / "certificate.cnf"
        config.write_text(
            "[req]\ndistinguished_name=dn\nx509_extensions=v3\nprompt=no\n"
            "[dn]\nCN=Gateway integration fixture\n[v3]\n"
            "subjectAltName=IP:127.0.0.1\nbasicConstraints=critical,CA:TRUE\n"
            "keyUsage=critical,digitalSignature,keyEncipherment,keyCertSign\n"
            "extendedKeyUsage=serverAuth\n"
        )
        subprocess.run([
            "openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes",
            "-days", "1", "-config", str(config),
            "-keyout", str(self.directory / "ca.key"),
            "-out", str(self.directory / "ca.crt"),
        ], check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        os.chmod(self.directory / "ca.key", 0o600)
        self.upstream = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Upstream)
        self.thread = threading.Thread(target=self.upstream.serve_forever)
        self.thread.start()
        self.addCleanup(self.close_upstream)
        # Check a free local port before starting the separate Node listener.
        with socket.socket() as listener:
            listener.bind(("127.0.0.1", 0))
            self.port = listener.getsockname()[1]
        self.process = subprocess.Popen([
            "node", str(ROOT / "scripts/fixtures/identity-tls-proxy.cjs"),
        ], env=dict(
            os.environ,
            FIXTURE_TLS_PORT=str(self.port),
            UPSTREAM_HTTP_PORT=str(self.upstream.server_port),
            FIXTURE_CERT_DIR=str(self.directory), FIXTURE_READY_SIGNAL="true",
        ), stdout=subprocess.PIPE, stderr=subprocess.DEVNULL)
        self.addCleanup(self.close_process)
        with selectors.DefaultSelector() as selector:
            selector.register(self.process.stdout, selectors.EVENT_READ)
            self.assertTrue(selector.select(timeout=10), "HTTPS fixture did not start")
            self.assertEqual(self.process.stdout.readline(), b"ready\n")
        self.context = ssl.create_default_context(cafile=str(self.directory / "ca.crt"))

    def close_upstream(self):
        self.upstream.shutdown()
        self.upstream.server_close()
        self.thread.join(timeout=5)
        self.assertFalse(self.thread.is_alive())

    def close_process(self):
        if self.process.poll() is None:
            self.process.terminate()
        try:
            self.process.wait(timeout=5)
        except subprocess.TimeoutExpired:
            self.process.kill()
            self.process.wait(timeout=5)
            self.fail("HTTPS fixture did not stop on SIGTERM")
        finally:
            self.process.stdout.close()

    def test_trusted_https_forwards_discovery_and_does_not_log_requests(self):
        path = "/realms/fixture/.well-known/openid-configuration"
        with urllib.request.urlopen(
            "https://127.0.0.1:" + str(self.port) + path,
            context=self.context, timeout=5,
        ) as response:
            self.assertEqual(response.status, 200)
            self.assertEqual(json.load(response), {"path": path, "proto": "https"})
        self.process.terminate()
        self.process.wait(timeout=5)
        self.assertEqual(self.process.stdout.read(), b"")

    def test_untrusted_certificate_is_rejected(self):
        with self.assertRaises(urllib.error.URLError) as raised:
            urllib.request.urlopen(
                "https://127.0.0.1:" + str(self.port) + "/health",
                context=ssl.create_default_context(), timeout=5,
            )
        self.assertIsInstance(raised.exception.reason, ssl.SSLCertVerificationError)

    def test_identity_http_client_explicitly_loads_fixture_ca(self):
        with patch.dict(os.environ, {"SSL_CERT_FILE": str(self.directory / "ca.crt")}):
            status, body, _ = identity.request_http("https://127.0.0.1:" + str(self.port) + "/fixture")
        self.assertEqual(status, 200)
        self.assertEqual(json.loads(body), {"path": "/fixture", "proto": "https"})

    def test_wrong_hostname_is_rejected_even_with_trusted_ca(self):
        with self.assertRaises(urllib.error.URLError) as raised:
            urllib.request.urlopen(
                "https://localhost:" + str(self.port) + "/health",
                context=self.context, timeout=5,
            )
        self.assertIsInstance(raised.exception.reason, ssl.SSLCertVerificationError)


if __name__ == "__main__":
    unittest.main()
