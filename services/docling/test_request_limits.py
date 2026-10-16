import asyncio
import os
import json
import base64
import unittest

from request_limits import RequestLimits


class RequestLimitsTest(unittest.TestCase):
    def request(self, path, method="GET", owner=b"a" * 64, body=None):
        if body is None:
            body = json.dumps({"sources":[{"kind":"file","filename":"test.pdf","base64_string":base64.b64encode(b"%PDF-test").decode()}],"options":{"to_formats":["md"],"image_export_mode":"placeholder","do_ocr":True,"abort_on_error":True}}).encode()
        sent = []
        scope = {"type": "http", "method": method, "path": path, "headers": [(b"x-tenant-id", owner),(b"x-api-key", os.environ["DOCLING_SERVE_API_KEY"].encode())]}
        async def receive():
            return {"type": "http.request", "body": body, "more_body": False}
        async def send(message):
            sent.append(message)
        async def downstream(scope, receive, send):
            await receive()
            await send({"type": "http.response.start", "status": 200, "headers": []})
            await send({"type": "http.response.body", "body": b"ok"})
        asyncio.run(RequestLimits(downstream)(scope, receive, send))
        return sent[0]["status"]

    def test_surface_and_owner(self):
        self.assertEqual(self.request("/ready", owner=b""), 200)
        self.assertEqual(self.request("/v1/convert/source/async", "POST"), 200)
        self.assertEqual(self.request("/v1/convert/source/async", "POST", owner=b""), 400)
        self.assertEqual(self.request("/v1/convert/file", "POST"), 404)
        self.assertEqual(self.request("/docs"), 404)

    def test_untrusted_conversion_options_and_sources(self):
        for body in [{"sources":[{"kind":"http","url":"http://example.invalid"}],"options":{}},{"sources":[],"options":{},"callbacks":[{}]}]:
            self.assertEqual(self.request("/v1/convert/source/async", "POST", body=json.dumps(body).encode()), 400)

    def test_body_limit_before_json_decoding(self):
        self.assertEqual(self.request("/v1/convert/source/async", "POST", body=b"x" * ((24 << 20) + 1)), 413)


if __name__ == "__main__":
    unittest.main()
