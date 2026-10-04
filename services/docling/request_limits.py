"""Internal API boundary: authenticate, bound bytes, allow only uploaded PDFs."""
import base64
import binascii
import hmac
import json
import os
import re

from starlette.responses import JSONResponse


class BodyTooLarge(Exception):
    pass


def valid_conversion(body):
    try:
        data = json.loads(body)
        if not isinstance(data, dict) or set(data) != {"sources", "options"}:
            return False
        options = {"to_formats": ["md"], "image_export_mode": "placeholder", "do_ocr": True, "abort_on_error": True}
        if data["options"] != options:
            return False
        sources = data["sources"]
        if not isinstance(sources, list) or len(sources) != 1:
            return False
        file = sources[0]
        if not isinstance(file, dict) or set(file) != {"kind", "filename", "base64_string"} or file["kind"] != "file":
            return False
        if not isinstance(file["filename"], str) or not re.fullmatch(r"[A-Za-z0-9_.-]{1,250}\.pdf", file["filename"]):
            return False
        raw = base64.b64decode(file["base64_string"], validate=True)
        return raw.startswith(b"%PDF-") and len(raw) <= 16 << 20
    except (ValueError, TypeError, KeyError, binascii.Error, RecursionError):
        return False


class RequestLimits:
    def __init__(self, app):
        self.app = app
        self.key = os.environ["DOCLING_SERVE_API_KEY"].encode()

    async def __call__(self, scope, receive, send):
        if scope["type"] == "lifespan":
            return await self.app(scope, receive, send)
        if scope["type"] != "http":
            return await send({"type": "websocket.close", "code": 1008})
        method, path = scope["method"], scope["path"]
        probe = method == "GET" and path in ("/health", "/ready", "/livez", "/readyz")
        submit = method == "POST" and path == "/v1/convert/source/async"
        task_read = method == "GET" and re.fullmatch(r"/v1/(status/poll|result)/[a-f0-9-]{36}", path)
        headers = dict(scope["headers"])
        if not probe and not hmac.compare_digest(headers.get(b"x-api-key", b""), self.key):
            return await JSONResponse({"detail": "unauthorized"}, status_code=401)(scope, receive, send)
        if not probe and not (submit or task_read):
            return await JSONResponse({"detail": "not found"}, status_code=404)(scope, receive, send)
        if not probe and not re.fullmatch(b"[a-f0-9]{64}", headers.get(b"x-tenant-id", b"")):
            return await JSONResponse({"detail": "owner scope required"}, status_code=400)(scope, receive, send)
        if not submit:
            return await self.app(scope, receive, send)
        chunks, size = [], 0
        while True:
            message = await receive()
            if message["type"] == "http.disconnect":
                return
            chunk = message.get("body", b"")
            size += len(chunk)
            if size > 24 << 20:
                return await JSONResponse({"detail": "request too large"}, status_code=413)(scope, receive, send)
            chunks.append(chunk)
            if not message.get("more_body", False):
                break
        body = b"".join(chunks)
        if not valid_conversion(body):
            return await JSONResponse({"detail": "invalid document conversion request"}, status_code=400)(scope, receive, send)
        delivered = False

        async def replay_receive():
            nonlocal delivered
            if delivered:
                return await receive()
            delivered = True
            return {"type": "http.request", "body": body, "more_body": False}

        await self.app(scope, replay_receive, send)
