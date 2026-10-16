#!/usr/bin/env python3
"""Real, opt-in LibreChat/Keycloak/Gateway/PostgreSQL acceptance scenario.

Uses the existing disposable identity harness, never developer credentials.
Images must already be present: this script never downloads images implicitly.
Raw OAuth/service logs remain private and must not be published as artifacts.
"""

import concurrent.futures
import copy
import html
import http.cookiejar
import importlib.util
import io
import json
import os
from pathlib import Path
import re
import secrets
import ssl
import subprocess
import sys
import traceback
import urllib.parse
import urllib.request
import uuid


ROOT = Path(__file__).resolve().parent.parent
LIBRECHAT_IMAGE = "ghcr.io/librechat-ai/librechat:v0.8.8@sha256:5b706bcee1ca154a1e2987303aaeca0eb27f97919cccee4bb604226c2f8abf61"
MONGO_IMAGE = "mongo:8.0.20@sha256:098862b1339f031900ca66cf8fef799e616d6324fa41b9a263f2ec899552c1ef"
# These are browser UI routes, not the separate headless Agents API. LibreChat
# deliberately rejects non-browser user agents before starting a generation.
BROWSER_USER_AGENT = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
spec = importlib.util.spec_from_file_location("identity_integration", Path(__file__).with_name("test-identity-integration.py"))
identity = importlib.util.module_from_spec(spec)
spec.loader.exec_module(identity)
check = identity.check
request_http = identity.request_http


class ObservedProvider(identity.Provider):
    rejected_markers = set()
    document_deliveries = {}
    privacy_deliveries = {}
    dlp_denials = set()
    dlp_available = True

    def do_POST(self):
        # Record only non-sensitive synthetic test markers, never prompts or
        # tokens. Per-request markers avoid races with background title calls.
        body = self.rfile.read(int(self.headers.get("Content-Length", "0")))
        if self.path == "/dlp/scan":
            value = json.loads(body)
            content = value.get("content", "")
            valid = set(value) <= {"request_id", "content"} and bool(value.get("request_id")) and not self.headers.get("Authorization")
            allowed = "LC_DLP_DENY" not in content
            with self.lock:
                if not allowed:
                    self.dlp_denials.update(re.findall(r"LC_REJECT_[0-9a-f]{32}", content))
                available = self.dlp_available
            self.send_response(200 if valid and available else 503)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(json.dumps({"allowed": allowed}).encode())
            return
        markers = re.findall(r"LC_REJECT_[0-9a-f]{32}", body.decode())
        document_requests = re.findall(r"LC_DOC_REQUEST_[0-9a-f]{32}", body.decode())
        documents = set(re.findall(r"LC_DOCUMENT_[0-9a-f]{32}", body.decode()))
        with self.lock:
            self.rejected_markers.update(markers)
            for marker in document_requests:
                self.document_deliveries.setdefault(marker, set()).update(documents)
            for marker in re.findall(r"LC_PRIVACY_[0-9a-f]{32}", body.decode()):
                observed = self.privacy_deliveries.setdefault(marker, set())
                if b"qa@example.test" in body:
                    observed.add("raw_email")
                if re.search(rb"\{\{EMAIL_[1-9][0-9]*\}\}", body):
                    observed.add("masked_email")
        original = self.rfile
        try:
            self.rfile = io.BytesIO(body)
            super().do_POST()
        finally:
            self.rfile = original


identity.Provider = ObservedProvider


def profile():
    return json.loads((ROOT / "examples/librechat/librechat.yaml").read_text())


def environment():
    text = (ROOT / "examples/librechat/.env.example").read_text()
    return dict(line.split("=", 1) for line in text.splitlines() if line and not line.startswith("#"))


def existing_images(docker):
    for image in (identity.KEYCLOAK_IMAGE, LIBRECHAT_IMAGE, MONGO_IMAGE):
        result = subprocess.run(docker + ["image", "inspect", image], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        check(result.returncode == 0, "required test image is missing; obtain permission and pull the pinned images explicitly")


def completed_chat(data):
    """Require a successful terminal message, not just a partial text delta."""
    for line in data.decode().splitlines():
        if not line.startswith("data: "):
            continue
        event = json.loads(line[6:])
        if not isinstance(event, dict) or event.get("final") is not True:
            continue
        response = event.get("responseMessage")
        if not isinstance(response, dict):
            continue
        if event.get("error") or event.get("reconcile") or response.get("error") or response.get("unfinished"):
            continue
        if "test response" in json.dumps(response):
            return True
    return False


class Run(identity.Run):
    def __init__(self):
        super().__init__()
        self.upstream_kc_port = identity.free_port()
        self.kc = "https://127.0.0.1:" + str(self.kc_port)
        self.issuer = self.kc + "/realms/gateway-users"
        self.tls = self.work / "tls"
        self.tls.mkdir(mode=0o700)
        config = self.tls / "certificate.cnf"
        config.write_text("[req]\ndistinguished_name=dn\nx509_extensions=v3\nprompt=no\n[dn]\nCN=Gateway integration fixture\n[v3]\nsubjectAltName=IP:127.0.0.1\nbasicConstraints=critical,CA:TRUE\nkeyUsage=critical,digitalSignature,keyEncipherment,keyCertSign\nextendedKeyUsage=serverAuth\n")
        self.command(["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "1", "-config", str(config), "-keyout", str(self.tls / "ca.key"), "-out", str(self.tls / "ca.crt")])
        os.chmod(self.tls / "ca.key", 0o600)
        os.chmod(self.tls / "ca.crt", 0o644)
        # Python and Node trust the disposable CA; Linux Go does too. Darwin
        # serves public signing keys on loopback (see container/launch below).
        # Browser OAuth retains certificate and hostname verification.
        self.previous_ca = os.environ.get("SSL_CERT_FILE")
        os.environ["SSL_CERT_FILE"] = str(self.tls / "ca.crt")
        self.env["SSL_CERT_FILE"] = str(self.tls / "ca.crt")

    def container(self, name, image, options, command=()):
        if image != identity.KEYCLOAK_IMAGE:
            return super().container(name, image, options, command)
        options = ["KC_HTTP_PORT=" + str(self.upstream_kc_port) if item.startswith("KC_HTTP_PORT=") else item for item in options]
        options += ["-e", "KC_PROXY_HEADERS=xforwarded"]
        if sys.platform == "darwin":
            # Darwin Go uses Keychain rather than SSL_CERT_FILE. Keep the real
            # browser OAuth flow HTTPS; serve public test signing keys only on
            # loopback instead of changing Keychain or disabling TLS checks.
            options += ["-p", "127.0.0.1:%d:%d" % (self.upstream_kc_port, self.upstream_kc_port)]
        super().container(name, image, options, command)
        super().container("ai-gateway-librechat-tls-" + self.suffix, LIBRECHAT_IMAGE,
                          ["--network", "container:" + name, "--user", "0:0", "--memory", "128m", "--entrypoint", "node",
                           "--mount", "type=bind,src=" + str(self.tls) + ",dst=/fixture,readonly",
                           "--mount", "type=bind,src=" + str(ROOT / "scripts/fixtures/identity-tls-proxy.cjs") + ",dst=/proxy.cjs,readonly",
                           "-e", "FIXTURE_TLS_PORT=" + str(self.kc_port), "-e", "UPSTREAM_HTTP_PORT=" + str(self.upstream_kc_port)],
                          ("/proxy.cjs",))

    def launch(self, service, settings):
        if service == "auth" and sys.platform == "darwin":
            settings = dict(settings, AUTH_JWT_JWKS_URL="http://127.0.0.1:" + str(self.upstream_kc_port) + "/realms/gateway-users/protocol/openid-connect/certs")
        if service == "gateway":
            providers = json.loads(settings["PROVIDERS_JSON"])
            for provider in providers:
                provider.update(dlp_enabled=True, anonymization="custom", anonymization_rules=["email"])
            settings = dict(settings, PROVIDERS_JSON=json.dumps(providers), DLP_REQUIRED="true",
                            DLP_URL="http://127.0.0.1:" + str(self.provider.server_port) + "/dlp")
        return super().launch(service, settings)

    def close(self):
        try:
            super().close()
        finally:
            if self.previous_ca is None:
                os.environ.pop("SSL_CERT_FILE", None)
            else:
                os.environ["SSL_CERT_FILE"] = self.previous_ca

    def setup(self):
        super().setup()
        # Keep the production realm HTTPS-only; override this isolated client
        # through Keycloak's admin API after the common harness imports it.
        self.ui = "http://127.0.0.1:" + str(self.ui_port)
        admin = {"Authorization": "Bearer " + self.admin_token}
        status, data, _ = request_http(self.kc + "/admin/realms/gateway-users/clients?clientId=librechat", headers=admin)
        check(status == 200 and len(json.loads(data)) == 1, "LibreChat OIDC client is missing")
        client = json.loads(data)[0]
        client.update(secret=self.client_secret, redirectUris=[self.ui + "/oauth/openid/callback"], webOrigins=[self.ui])
        status, _, _ = request_http(self.kc + "/admin/realms/gateway-users/clients/" + client["id"], client, admin, method="PUT")
        check(status == 204, "isolated LibreChat callback configuration failed")

    def launch_librechat(self):
        mongo_port = identity.free_port()
        self.mongo_name = "ai-gateway-librechat-mongo-" + self.suffix
        # Both processes share only the fixture Keycloak network namespace.
        # MongoDB has no host port and is never part of the working deployment.
        self.container(self.mongo_name, MONGO_IMAGE,
                       ["--network", "container:" + self.kc_name, "--memory", "1g"],
                       ("mongod", "--bind_ip", "127.0.0.1", "--port", str(mongo_port)))
        if sys.platform == "darwin":
            host = "host.docker.internal"
        else:
            host = self.command(self.docker + ["network", "inspect", "bridge", "--format", "{{(index .IPAM.Config 0).Gateway}}"]).decode().strip()
        config_path = self.work / "librechat.yaml"
        config = profile()
        # The upstream schema requires a non-empty fallback list. Use only the
        # common, genuinely provisioned fixture model, not either private model.
        config["endpoints"]["custom"][0]["models"]["default"] = ["model-common"]
        config_path.write_text(json.dumps(config))
        os.chmod(config_path, 0o644)  # Public configuration; contains no tokens.
        # The root private temp directory is not mounted into any container.
        # Bind-mounting this single public file does not expose logs or secrets.
        settings = environment()
        settings.update(
            DOMAIN_CLIENT=self.ui, DOMAIN_SERVER=self.ui, PORT=str(self.ui_port),
            AI_GATEWAY_BASE_URL="http://" + host + ":" + str(self.gw_port) + "/v1",
            MONGO_URI="mongodb://127.0.0.1:" + str(mongo_port) + "/librechat_integration",
            OPENID_ISSUER=self.issuer, OPENID_CLIENT_SECRET=self.client_secret,
            OPENID_REUSE_MAX_SESSION_AGE_MS="1", OPENID_CLOCK_TOLERANCE="0",
            SESSION_COOKIE_SECURE="false", NODE_ENV="production", SEARCH="false",
            SCHEDULES_SINGLE_PROCESS="true",
            NODE_EXTRA_CA_CERTS="/fixture/ca.crt",
            CREDS_KEY=secrets.token_hex(32), CREDS_IV=secrets.token_hex(16),
            JWT_SECRET=secrets.token_urlsafe(32), JWT_REFRESH_SECRET=secrets.token_urlsafe(32),
            OPENID_SESSION_SECRET=secrets.token_urlsafe(32),
        )
        check(not any(value.startswith("REQUIRED_") for value in settings.values()), "unresolved test configuration")
        options = ["--network", "container:" + self.kc_name, "--memory", "2g", "--cpus", "2", "--mount", "type=bind,src=" + str(config_path) + ",dst=/app/librechat.yaml,readonly",
                   "--mount", "type=bind,src=" + str(self.tls / "ca.crt") + ",dst=/fixture/ca.crt,readonly"]
        for key, value in settings.items():
            options += ["-e", key + "=" + value]
        self.ui_name = "ai-gateway-librechat-" + self.suffix
        self.container(self.ui_name, LIBRECHAT_IMAGE, options)
        identity.wait_ready(self.ui + "/health", seconds=240)

    def login(self, name):
        jar = http.cookiejar.CookieJar(policy=identity.LoopbackCookiePolicy())
        opener = urllib.request.build_opener(
            urllib.request.HTTPCookieProcessor(jar),
            urllib.request.HTTPSHandler(context=ssl.create_default_context(cafile=str(self.tls / "ca.crt"))),
        )
        opener.addheaders = [("User-Agent", BROWSER_USER_AGENT)]
        status, page, _ = request_http(self.ui + "/oauth/openid", opener=opener)
        form = re.search(r'<form[^>]*id="kc-form-login"[^>]*action="([^"]+)"', page.decode())
        check(status == 200 and form is not None, "LibreChat did not start real Keycloak browser authorization")
        status, _, _ = request_http(html.unescape(form.group(1)), urllib.parse.urlencode({"username": name, "password": self.password, "credentialId": ""}).encode(), {"Content-Type": "application/x-www-form-urlencoded"}, opener=opener)
        check(status == 200, "LibreChat OAuth callback failed")
        status, data, _ = request_http(self.ui + "/api/auth/refresh", {}, {"Origin": self.ui}, opener=opener)
        check(status == 200 and json.loads(data).get("token"), "LibreChat did not establish an authenticated OpenID session")
        return {"opener": opener, "token": json.loads(data)["token"], "user": json.loads(data).get("user", {})}

    def ui_api(self, session, path, body=None, method=None):
        return request_http(self.ui + path, body, {"Authorization": "Bearer " + session["token"], "Origin": self.ui, "User-Agent": BROWSER_USER_AGENT, "X-LibreChat-Generation-Protocol": "2"}, method, opener=session["opener"])

    def chat(self, session, model, text, files=None, agent=None):
        conversation = "new"
        body = {
            "endpoint": "AI Gateway", "endpointType": "custom", "model": model,
            "text": text, "conversationId": conversation, "messageId": str(uuid.uuid4()),
            "parentMessageId": "00000000-0000-0000-0000-000000000000",
            "isContinued": False, "isTemporary": False, "max_tokens": 16,
        }
        if files:
            body["files"] = files
        path = "/api/agents/chat/AI%20Gateway"
        if agent:
            body.update(endpoint="agents", endpointType="agents", agent_id=agent)
            path = "/api/agents/chat"
        status, data, headers = self.ui_api(session, path, body)
        if status != 200:
            return status, data, headers, conversation
        if "text/event-stream" in headers.get("Content-Type", "") or data.startswith(b"event: error\n"):
            # Access middleware can emit a terminal SSE error before a job is
            # created. Keep that denial visible instead of mistaking it for an
            # accepted generation (or trying to subscribe to a missing stream).
            return status, data, headers, conversation
        if "application/json" not in headers.get("Content-Type", ""):
            # Keep diagnostic content private; middleware errors can omit a
            # content type. Never include response bodies in CI output.
            diagnostic = self.work / "chat-start-response.txt"
            fd = os.open(diagnostic, os.O_CREAT | os.O_WRONLY | os.O_TRUNC, 0o600)
            with os.fdopen(fd, "wb") as output:
                output.write(data)
        # v0.8.8 starts a resumable job, then the browser subscribes separately.
        # An accepted job is not proof of a successful upstream execution.
        check("application/json" in headers.get("Content-Type", ""), "LibreChat did not return a generation start response; content type=" + headers.get("Content-Type", "missing") + "; body bytes=" + str(len(data)) + "; header names=" + ",".join(headers.keys()))
        started = json.loads(data)
        if started.get("error"):
            return status, data, headers, conversation
        check(started.get("status") == "started", "LibreChat did not start a fresh generation")
        stream = started.get("streamId")
        conversation = started.get("conversationId")
        generation = started.get("generationCreatedAt")
        check(isinstance(stream, str) and 0 < len(stream) <= 512, "invalid LibreChat stream identity")
        check(isinstance(conversation, str) and 0 < len(conversation) <= 128, "invalid LibreChat conversation identity")
        check(type(generation) is int and 0 <= generation <= 9007199254740991, "invalid LibreChat generation identity")
        path = "/api/agents/chat/stream/" + urllib.parse.quote(stream, safe="")
        path += "?" + urllib.parse.urlencode({"generationCreatedAt": generation})
        status, data, headers = self.ui_api(session, path)
        return status, data, headers, conversation

    def assert_rejected(self, session, model, reason, prefix=""):
        marker = "LC_REJECT_" + uuid.uuid4().hex
        status, data, _, _ = self.chat(session, model, prefix + marker)
        check(status in (400, 401, 403, 429) or b"error" in data.lower(), reason + ": UI did not report failure")
        with identity.Provider.lock:
            check(marker not in identity.Provider.rejected_markers, reason + ": provider executed a rejected request")
        return marker

    def privacy_checks(self, sessions):
        marker = "LC_PRIVACY_" + uuid.uuid4().hex
        status, data, _, conversation = self.chat(sessions["alice"], "model-a", marker + " qa@example.test")
        check(status == 200 and completed_chat(data), "masked LibreChat request failed")
        with identity.Provider.lock:
            observed = identity.Provider.privacy_deliveries.get(marker, set())
            check("masked_email" in observed and "raw_email" not in observed, "Gateway did not mask PII before model execution")
        status, history, _ = self.ui_api(sessions["alice"], "/api/messages/" + conversation)
        check(status == 200 and b"qa@example.test" in history, "UI storage boundary was incorrectly treated as Gateway-masked history")
        rejected = self.assert_rejected(sessions["alice"], "model-a", "Gateway DLP was bypassed", prefix="LC_DLP_DENY ")
        with identity.Provider.lock:
            check(rejected in identity.Provider.dlp_denials, "Gateway did not invoke DLP for the rejected request")
            identity.Provider.dlp_available = False
        try:
            self.assert_rejected(sessions["alice"], "model-a", "required DLP failure did not fail closed")
        finally:
            with identity.Provider.lock:
                identity.Provider.dlp_available = True
        leaked = self.sql(self.billing_dsn, "SELECT count(*) FROM billing_outbox WHERE payload::text LIKE '%qa@example.test%' OR payload::text LIKE '%LC_DOCUMENT_%';")
        check(leaked == "0", "billing outbox received prompt or document content")
        print("PASS Gateway-side PII masking, required DLP deny/failure and content-free billing outbox", flush=True)

    def upload_text(self, session, marker):
        boundary = "gateway-fixture-" + secrets.token_hex(16)
        fields = {
            "endpoint": "AI Gateway", "endpointType": "custom", "model": "model-a",
            "file_id": str(uuid.uuid4()), "message_file": "true", "tool_resource": "context",
        }
        parts = []
        for name, value in fields.items():
            parts.append(("--" + boundary + '\r\nContent-Disposition: form-data; name="' + name + '"\r\n\r\n' + value + "\r\n").encode())
        parts.append(("--" + boundary + '\r\nContent-Disposition: form-data; name="file"; filename="fixture.txt"\r\nContent-Type: text/plain\r\n\r\nDocument token: ' + marker + "\r\n").encode())
        parts.append(("--" + boundary + "--\r\n").encode())
        status, data, _ = request_http(self.ui + "/api/files", b"".join(parts),
                                       {"Authorization": "Bearer " + session["token"], "Origin": self.ui,
                                        "Content-Type": "multipart/form-data; boundary=" + boundary},
                                       opener=session["opener"])
        check(status == 200, "LibreChat text upload failed")
        payload = json.loads(data)
        file = payload.get("file", payload)
        check(isinstance(file, dict) and file.get("file_id") and file.get("user"), "LibreChat upload did not return an owned file")
        return file

    def file_checks(self, sessions):
        marker = "LC_DOCUMENT_" + uuid.uuid4().hex
        file = self.upload_text(sessions["alice"], marker)
        file_id = file["file_id"]
        for name in ("alice", "bob"):
            status, data, _ = self.ui_api(sessions[name], "/api/files")
            check(status == 200, "LibreChat file list failed")
            found = any(item.get("file_id") == file_id for item in json.loads(data))
            check(found == (name == "alice"), "LibreChat file list crossed ownership")
        path = "/api/files/download/" + urllib.parse.quote(file["user"], safe="") + "/" + urllib.parse.quote(file_id, safe="")
        status, _, _ = self.ui_api(sessions["bob"], path)
        check(status in (403, 404), "LibreChat allowed a foreign file download")
        attachment = {key: file[key] for key in ("file_id", "type", "filename") if key in file}
        own_request = "LC_DOC_REQUEST_" + uuid.uuid4().hex
        status, data, _, _ = self.chat(sessions["alice"], "model-a", own_request, [attachment])
        check(status == 200 and completed_chat(data), "LibreChat own attachment chat failed")
        with identity.Provider.lock:
            check(marker in identity.Provider.document_deliveries.get(own_request, set()), "uploaded document text never reached Gateway/provider; request observed=" + str(own_request in identity.Provider.document_deliveries) + "; upload has text=" + str(bool(file.get("text"))) + "; delivery=" + str(file.get("llmDeliveryPath")))
        foreign_request = "LC_DOC_REQUEST_" + uuid.uuid4().hex
        self.chat(sessions["bob"], "model-b", foreign_request, [attachment])
        with identity.Provider.lock:
            check(marker not in identity.Provider.document_deliveries.get(foreign_request, set()), "foreign file text leaked to another caller's model execution")
        print("PASS LibreChat text upload, file-list/download ownership and model-bound attachment isolation", flush=True)

    def agent_checks(self, sessions):
        status, data, _ = self.ui_api(sessions["alice"], "/api/agents", {
            "name": "Gateway identity fixture", "provider": "AI Gateway", "model": "model-a",
            "instructions": "Reply briefly.", "tools": [], "model_parameters": {"max_tokens": 16},
        })
        check(status == 201 and json.loads(data).get("id"), "LibreChat saved agent creation failed")
        agent = json.loads(data)["id"]
        status, _, _ = self.ui_api(sessions["bob"], "/api/agents/" + urllib.parse.quote(agent, safe=""))
        check(status in (403, 404), "LibreChat exposed an unshared saved agent")
        status, data, _, _ = self.chat(sessions["alice"], "model-a", "saved agent fixture", agent=agent)
        check(status == 200 and completed_chat(data), "saved LibreChat agent did not execute through Gateway")
        restricted = copy.deepcopy(self.policies["alice"])
        restricted["allowed_models"] = ["model-common"]
        self.internal("PUT", "/jwt-principals", restricted)
        try:
            rejected = "LC_REJECT_" + uuid.uuid4().hex
            status, data, _, _ = self.chat(sessions["alice"], "model-a", rejected, agent=agent)
            check(status in (400, 401, 403) or b"error" in data.lower(), "saved agent ignored revoked model grants")
            with identity.Provider.lock:
                check(rejected not in identity.Provider.rejected_markers, "saved agent bypassed current Gateway model grants")
        finally:
            self.internal("PUT", "/jwt-principals", self.policies["alice"])
        print("PASS saved LibreChat agent ownership and live Gateway model-grant enforcement", flush=True)

    def checks(self):
        self.launch_librechat()
        # Bootstrap the UI-only operator first; both inference accounts must be
        # ordinary LibreChat users rather than the first-user administrator.
        sessions = {name: self.login(name) for name in ("operator", "alice", "bob")}
        for name in ("alice", "bob"):
            # LibreChat may return an ID token for its own browser session.
            # The inference credential is the server-side ACCESS_TOKEN template,
            # whose identity is proved by model grants and durable billing below.
            claims = identity.token_claims(sessions[name]["token"])
            audience = claims.get("aud", [])
            audience = [audience] if isinstance(audience, str) else audience
            if "gateway" not in audience:
                check(self.authorize(sessions[name]["token"])[0] == 401, "Gateway accepted a LibreChat UI/ID token")
        print("PASS LibreChat real browser OAuth/PKCE and rejection of UI-only credentials", flush=True)

        def model_check(name):
            expected = {"model-a" if name == "alice" else "model-b", "model-common"}
            status, data, _ = self.ui_api(sessions[name], "/api/models")
            check(status == 200 and set(json.loads(data).get("AI Gateway", [])) == expected, "LibreChat model catalog crossed caller grants")
        for name in ("alice", "bob", "alice", "bob"):
            model_check(name)
        with concurrent.futures.ThreadPoolExecutor(max_workers=4) as executor:
            list(executor.map(model_check, ["alice", "bob"] * 3))

        for name, model, hidden in (("alice", "model-a", "model-b"), ("bob", "model-b", "model-a")):
            session = sessions[name]
            status, data, headers, conversation = self.chat(session, model, "LibreChat fixture " + name)
            check(status == 200 and "text/event-stream" in headers.get("Content-Type", "") and completed_chat(data), "full LibreChat SSE chat did not complete successfully")
            status, history, _ = self.ui_api(session, "/api/messages/" + conversation)
            check(status == 200 and b"test response" in history, "LibreChat history was not persisted")
            other = sessions["bob" if name == "alice" else "alice"]
            status, history, _ = self.ui_api(other, "/api/messages/" + conversation)
            check(status in (200, 403, 404) and b"test response" not in history, "LibreChat leaked another user's history")
            self.assert_rejected(session, hidden, "saved UI model selection bypassed Gateway grants")
            status, refreshed, _ = self.ui_api(session, "/api/auth/refresh", {})
            check(status == 200 and json.loads(refreshed).get("token"), "LibreChat server refresh failed")
            session["token"] = json.loads(refreshed)["token"]
            status, data, _, _ = self.chat(session, model, "refreshed caller fixture " + name)
            check(status == 200 and completed_chat(data), "LibreChat inference failed after OAuth refresh")
            model_check(name)
        print("PASS per-user model cache, concurrent catalog, full SSE chat/history, hidden models and refresh", flush=True)
        self.file_checks(sessions)
        self.agent_checks(sessions)
        self.privacy_checks(sessions)

        # No valid shared credential exists. Even a real authenticated UI user
        # without a Gateway binding cannot cause upstream execution.
        self.assert_rejected(sessions["operator"], "model-common", "missing binding fell back to a shared key")
        for name in ("alice", "bob"):
            user = self.policies[name]["user_id"]
            # IDs are fixture-generated; never interpolate user-provided SQL.
            check(re.fullmatch(r"identity-[a-f0-9]+-(alice|bob)", user) is not None, "unsafe fixture identifier")
            result = self.sql(self.billing_dsn, "SELECT count(DISTINCT credential_id),count(*),sum(actual_tokens) FROM billing_budget_reservations WHERE user_id='" + user + "' AND state='committed';").split("|")
            check(result[0] == "1" and int(result[1]) > 0 and int(result[2]) > 0, "LibreChat billing attribution missing or split")
        limited = copy.deepcopy(self.policies["bob"])
        limited["rate_limit_rpm"] = 1
        self.internal("PUT", "/jwt-principals", limited)
        self.assert_rejected(sessions["bob"], "model-b", "Gateway quota failure was hidden or bypassed")
        self.internal("PUT", "/jwt-principals", self.policies["bob"])
        self.internal("PUT", "/users/" + self.policies["alice"]["user_id"], {"status": "disabled", "roles": ["user"]})
        self.assert_rejected(sessions["alice"], "model-a", "live UI session bypassed deprovisioning")
        self.internal("PUT", "/users/" + self.policies["alice"]["user_id"], {"status": "active", "roles": ["user"]})
        print("PASS PostgreSQL per-user attribution, Gateway quotas, no shared fallback and live-session deprovisioning", flush=True)


def main():
    check(os.getenv("IDENTITY_INTEGRATION_TESTS") == "true", "set IDENTITY_INTEGRATION_TESTS=true for disposable integration databases")
    for key in ("AUTH_POSTGRES_TEST_DSN", "BILLING_POSTGRES_TEST_DSN", "CONTROL_PLANE_POSTGRES_TEST_DSN"):
        check(os.getenv(key), "dedicated test DSN required: " + key)
    docker = ["docker"]
    if sys.platform == "darwin":
        identity.Run.command(["rdctl", "api", "/v1/backend_state"])
        docker += ["--context", "rancher-desktop"]
    existing_images(docker)
    run = Run()
    try:
        run.setup()
        run.checks()
        for name in ("alice", "bob"):
            run.tokens[name] = run.token(name)
        run.gateway_checks()
    finally:
        run.close()


if __name__ == "__main__":
    try:
        main()
    except Exception as error:
        # Never print library exception text, callback URLs, or response bodies.
        print("FAIL LibreChat integration: " + (str(error) if isinstance(error, RuntimeError) else type(error).__name__), file=sys.stderr)
        for frame in traceback.extract_tb(error.__traceback__):
            print("  " + Path(frame.filename).name + ":" + str(frame.lineno) + " " + frame.name, file=sys.stderr)
        sys.exit(1)
