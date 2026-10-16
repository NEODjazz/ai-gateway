"""Create an isolated processing stack, run real integration checks, then stop it.

No existing deployment, Secret, image, container or volume is removed.
The generated private env file is never printed or added to an image context.
"""
import os
from pathlib import Path
import secrets
import subprocess
import tempfile
import uuid

ROOT = Path(__file__).resolve().parents[2]


def main():
    project = "docling-check-" + uuid.uuid4().hex[:8]
    directory = Path(tempfile.mkdtemp(prefix="gateway-docling-check-"))
    env_file = directory / "runtime.env"
    key = secrets.token_urlsafe(32)
    fd = os.open(env_file, os.O_CREAT | os.O_WRONLY, 0o600)
    with os.fdopen(fd, "w") as f:
        f.write("DOCLING_API_KEY=" + key + "\nDOCLING_PORT=0\nDOCUMENT_QUEUE_CAPACITY=2\n")
    compose = ["docker", "compose", "-f", str(ROOT / "docker-compose.docling.yml"), "--env-file", str(env_file), "-p", project]
    api = project + "-docling-api-1"

    def run(command, **kwargs):
        kwargs.setdefault("cwd", ROOT)
        return subprocess.run(command, check=True, **kwargs)

    def script(name, *arguments):
        with open(ROOT / "services/docling" / name, "rb") as f:
            run(["docker", "exec", "-i", api, "python", "-B", "-", *arguments], stdin=f)

    try:
        run(["docker", "build", "-t", "ai-gateway-docling:local", "services/docling"])
        run(compose + ["up", "-d", "docling-redis", "docling-api"])
        script("test_admission.py")
        script("test_request_limits.py")
        script("integration.py", "queue")
        # Job ownership and task payloads must survive API and Redis restart.
        saved = run(["docker", "exec", api, "cat", "/tmp/document-integration.json"], capture_output=True).stdout
        run(compose + ["restart", "docling-redis", "docling-api"])
        run(["docker", "exec", "-i", api, "python", "-c", "import sys,pathlib;pathlib.Path('/tmp/document-integration.json').write_bytes(sys.stdin.buffer.read())"], input=saved)
        run(compose + ["up", "-d", "docling-worker"])
        script("integration.py", "convert")
        port = run(compose + ["port", "docling-api", "5001"], capture_output=True, text=True).stdout.strip()
        child = os.environ.copy()
        child.update(DOCLING_TEST_URL="http://" + port, DOCLING_TEST_API_KEY=key)
        run(["go", "test", "-tags=doclingintegration", "./internal/documentprocessing", "-run", "TestDoclingRealAPIAndWorker", "-count=1"], cwd=ROOT / "repos/gateway", env=child)
        print("PASS: durable task restart and Gateway Go client integration")
    finally:
        subprocess.run(compose + ["stop"], cwd=ROOT, check=False)
        print("Stopped isolated test stack:", project)


if __name__ == "__main__":
    main()
