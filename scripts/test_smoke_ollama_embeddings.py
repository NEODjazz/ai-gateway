import http.server
import json
import os
import pathlib
import subprocess
import threading
import unittest


SCRIPT = pathlib.Path(__file__).with_name("smoke-ollama-embeddings.sh")


class EmbeddingHandler(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        if self.headers.get("Authorization") != "Bearer test-only":
            self.send_error(401)
            return
        length = int(self.headers["Content-Length"])
        self.server.requests.append(json.loads(self.rfile.read(length)))
        body = {
            "object": "list",
            "model": "nomic-embed-text:latest",
            "data": [
                {"index": 0, "embedding": [0.1]},
                {"index": 1, "embedding": [0.2]},
            ],
            "usage": {"total_tokens": 2},
        }
        encoded = json.dumps(body).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(encoded)))
        self.end_headers()
        self.wfile.write(encoded)

    def log_message(self, format, *args):
        pass


class SmokeOllamaEmbeddingsTest(unittest.TestCase):
    def setUp(self):
        self.server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), EmbeddingHandler)
        self.server.requests = []
        self.thread = threading.Thread(target=self.server.serve_forever)
        self.thread.start()

    def tearDown(self):
        self.server.shutdown()
        self.server.server_close()
        self.thread.join()

    def run_smoke(self, provider=None, key="test-only"):
        env = os.environ.copy()
        env.pop("OLLAMA_EMBEDDING_PROVIDER", None)
        env.pop("API_KEY", None)
        env["GATEWAY_URL"] = f"http://127.0.0.1:{self.server.server_port}"
        if provider is not None:
            env["OLLAMA_EMBEDDING_PROVIDER"] = provider
        if key is not None:
            env["API_KEY"] = key
        return subprocess.run(
            ["bash", str(SCRIPT)],
            env=env,
            capture_output=True,
            text=True,
            timeout=10,
            check=False,
        )

    def test_default_uses_model_routing(self):
        result = self.run_smoke()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertNotIn("test-only", result.stdout + result.stderr)
        self.assertEqual(len(self.server.requests), 1)
        self.assertNotIn("provider", self.server.requests[0])

    def test_explicit_provider_is_preserved(self):
        result = self.run_smoke(provider="chosen-endpoint")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.server.requests[0]["provider"], "chosen-endpoint")

    def test_requires_test_credential(self):
        result = self.run_smoke(key=None)
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(self.server.requests, [])


if __name__ == "__main__":
    unittest.main()
