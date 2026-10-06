"""Real API/Redis/worker checks using generated, non-private PDFs.

Run `queue` with workers stopped, then `convert` after starting workers.
Never prints service credentials, uploaded content or raw error responses.
"""
import base64
import io
import json
import os
import sys
import time
import urllib.error
import urllib.request

URL = os.environ.get("DOCLING_TEST_URL", "http://127.0.0.1:5001")
KEY = os.environ["DOCLING_SERVE_API_KEY"]
OWNER = "a" * 64
STATE = "/tmp/document-integration.json"


def pdf(scanned=True):
    objects = [b"<< /Type /Catalog /Pages 2 0 R >>", b"<< /Type /Pages /Kids [3 0 R 4 0 R] /Count 2 >>",
               b"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 5 0 R >> >> /Contents 6 0 R >>",
               b"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /XObject << /Im1 8 0 R >> /Font << /F1 5 0 R >> >> /Contents 7 0 R >>",
               b"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>"]
    text = b"BT /F1 24 Tf 50 700 Td (GATEWAY TEXT PAGE) Tj ET"
    second = b"BT /F1 24 Tf 50 700 Td (GATEWAY SECOND PAGE) Tj ET"
    image = b""
    if scanned:
        from PIL import Image, ImageDraw, ImageFont
        page = Image.new("RGB", (1200, 1500), "white")
        draw = ImageDraw.Draw(page)
        font = ImageFont.load_default(size=48)
        draw.text((100, 150), "GATEWAY OCR SCAN", fill="black", font=font)
        draw.text((100, 250), "Local document conversion", fill="black", font=font)
        buf = io.BytesIO()
        page.save(buf, format="JPEG", quality=95)
        image = buf.getvalue()
        second = b"q 612 0 0 792 0 0 cm /Im1 Do Q"
    for stream in (text, second):
        objects.append(b"<< /Length " + str(len(stream)).encode() + b" >>\nstream\n" + stream + b"\nendstream")
    objects.append(b"<< /Type /XObject /Subtype /Image /Width 1200 /Height 1500 /ColorSpace /DeviceRGB /BitsPerComponent 8 /Filter /DCTDecode /Length " + str(len(image)).encode() + b" >>\nstream\n" + image + b"\nendstream")
    return serialize_pdf(objects)


def serialize_pdf(objects):
    data = bytearray(b"%PDF-1.4\n")
    offsets = [0]
    for i, obj in enumerate(objects, 1):
        offsets.append(len(data))
        data += f"{i} 0 obj\n".encode() + obj + b"\nendobj\n"
    start = len(data)
    data += f"xref\n0 {len(offsets)}\n0000000000 65535 f \n".encode()
    for offset in offsets[1:]:
        data += f"{offset:010d} 00000 n \n".encode()
    data += f"trailer\n<< /Size {len(offsets)} /Root 1 0 R >>\nstartxref\n{start}\n%%EOF\n".encode()
    return bytes(data)


def over_page_limit_pdf():
    count = 33
    kids = " ".join(f"{i} 0 R" for i in range(3, count + 3))
    objects = [b"<< /Type /Catalog /Pages 2 0 R >>",
               f"<< /Type /Pages /Kids [{kids}] /Count {count} >>".encode()]
    for _ in range(count):
        objects.append(f"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 {count + 3} 0 R >> >> /Contents {count + 4} 0 R >>".encode())
    objects.append(b"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>")
    text = b"BT /F1 24 Tf 50 700 Td (PAGE LIMIT TEST) Tj ET"
    objects.append(b"<< /Length " + str(len(text)).encode() + b" >>\nstream\n" + text + b"\nendstream")
    return serialize_pdf(objects)


def request(path, body=None, owner=OWNER, key=KEY):
    headers = {"X-Api-Key": key, "X-Tenant-ID": owner, "Content-Type": "application/json"}
    req = urllib.request.Request(URL + path, data=None if body is None else json.dumps(body).encode(), headers=headers)
    try:
        with urllib.request.urlopen(req, timeout=15) as response:
            return response.status, json.load(response)
    except urllib.error.HTTPError as exc:
        return exc.code, None


def submit(data):
    return request("/v1/convert/source/async", {"sources": [{"kind": "file", "filename": "test.pdf", "base64_string": base64.b64encode(data).decode()}], "options": {"to_formats": ["md"], "do_ocr": True, "abort_on_error": True, "image_export_mode": "placeholder"}})


def wait_ready():
    end = time.monotonic() + 90
    while time.monotonic() < end:
        try:
            if request("/ready")[0] == 200:
                return
        except urllib.error.URLError:
            pass
        time.sleep(0.5)
    raise AssertionError("API readiness deadline")


def wait(task, success=True):
    end = time.monotonic() + 240
    while time.monotonic() < end:
        status, data = request("/v1/status/poll/" + task)
        assert status == 200, "task status unavailable"
        if data["task_status"] in ("success", "failure"):
            if not success:
                assert data["task_status"] == "failure", "invalid PDF unexpectedly accepted"
                return
            assert data["task_status"] == "success", "conversion task failed"
            status, result = request("/v1/result/" + task)
            assert status == 200 and result["status"] == "success" and not result.get("errors"), "conversion incomplete"
            return result["document"]["md_content"]
        time.sleep(0.5)
    raise AssertionError("worker conversion deadline")


def main():
    wait_ready()
    if sys.argv[1] == "queue":
        assert request("/v1/convert/source/async", {}, key="invalid")[0] in (401, 403)
        assert request("/v1/convert/source/async", {"sources": [{"kind": "http", "url": "http://example.invalid/private.pdf"}]})[0] in (400, 422)
        tasks = []
        for _ in range(2):
            status, task = submit(pdf())
            assert status == 200, "failed to enqueue"
            tasks.append(task["task_id"])
            assert request("/v1/status/poll/" + tasks[-1])[1]["task_status"] == "pending"
        assert submit(pdf())[0] == 503, "queue capacity was not enforced"
        assert request("/v1/status/poll/" + tasks[0], owner="b" * 64)[0] == 404
        assert request("/v1/result/" + tasks[0], owner="b" * 64)[0] == 404
        with open(STATE, "w") as f:
            json.dump(tasks, f)
        print("PASS: stopped workers, bounded queue, owner isolation, forbidden URL source")
    else:
        with open(STATE) as f:
            tasks = json.load(f)
        for task in tasks:
            text = wait(task).upper()
            assert "GATEWAY TEXT PAGE" in text, "PDF text extraction missing"
            assert "GATEWAY OCR SCAN" in text, "scanned page OCR missing"
        status, task = submit(pdf(scanned=False))
        assert status == 200, "capacity did not recover after completion"
        text = wait(task["task_id"]).upper()
        assert "GATEWAY SECOND PAGE" in text, "second text page missing"
        print("PASS: workers consumed persisted queue, text extraction + real OCR, admission recovered")
        for invalid in (b"%PDF-invalid", over_page_limit_pdf()):
            status, task = submit(invalid)
            assert status == 200, "failed to enqueue rejection fixture"
            wait(task["task_id"], success=False)
        print("PASS: malformed PDF and page-count limit fail closed")


if __name__ == "__main__":
    main()
