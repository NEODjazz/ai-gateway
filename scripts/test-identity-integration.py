#!/usr/bin/env python3
"""Real Keycloak/Auth/Gateway/Billing integration on dedicated test databases.

Never reads developer credentials. Test secrets are generated in a private temp
folder; raw service logs and OAuth tokens must not be uploaded as CI artifacts.
Only this run's processes/containers are stopped; existing resources stay intact.
"""
import base64
import concurrent.futures
import copy
import hashlib
import html
import http.cookiejar
import http.server
import json
import os
from pathlib import Path
import re
import secrets
import socket
import subprocess
import sys
import tempfile
import threading
import traceback
import time
import urllib.error
import urllib.parse
import urllib.request

ROOT = Path(__file__).resolve().parent.parent
KEYCLOAK_IMAGE = "quay.io/keycloak/keycloak:26.3.3@sha256:6a7217a100bd3e5de4063a27a538ef999a3c5a88c4b4ec0ffc0a642aee7b2597"
OPENWEBUI_IMAGE = "ghcr.io/open-webui/open-webui:v0.11.4-slim@sha256:0487ad4a5a4b986062dedace806c3ef1e88fec38c10d1e64d6a5501c66671e5e"


def check(condition, message):
    if not condition:
        raise RuntimeError(message)


def free_port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def request_http(url, body=None, headers=None, method=None, opener=None):
    headers = dict(headers or {})
    if isinstance(body, dict):
        body = json.dumps(body).encode()
        headers.setdefault("Content-Type", "application/json")
    request = urllib.request.Request(url, data=body, headers=headers, method=method)
    try:
        response = (
            opener.open(request, timeout=10)
            if opener
            else urllib.request.urlopen(request, timeout=10)
        )
    except urllib.error.HTTPError as response:
        with response:
            return response.code, response.read(), response.headers
    with response:
        error = urllib.parse.parse_qs(
            urllib.parse.urlsplit(response.geturl()).query
        ).get("error", [""])[0]
        if error:
            raise RuntimeError(
                "OAuth redirect error: "
                + re.sub(r"[a-zA-Z0-9_-]{24,}|https?://[^ ]+", "[redacted]", error)
            )
        return response.status, response.read(), response.headers


def wait_ready(url, seconds=120):
    deadline = time.monotonic() + seconds
    while time.monotonic() < deadline:
        try:
            status, _, _ = request_http(url)
            if status in (200, 204):
                return
        except (OSError, urllib.error.URLError):
            pass
        time.sleep(0.5)
    raise RuntimeError("test service readiness timeout")


def token_claims(token):
    part = token.split(".")[1]
    return json.loads(base64.urlsafe_b64decode(part + "=" * (-len(part) % 4)))


class LoopbackCookiePolicy(http.cookiejar.DefaultCookiePolicy):
    # Browsers treat localhost as a secure context. urllib does not, so mirror
    # that exception only for this isolated loopback OAuth fixture.
    def return_ok_secure(self, cookie, request):
        if urllib.parse.urlsplit(request.full_url).hostname == "127.0.0.1":
            return True
        return super().return_ok_secure(cookie, request)


class Provider(http.server.BaseHTTPRequestHandler):
    calls = 0
    lock = threading.Lock()

    def log_message(self, *_):
        pass

    def do_POST(self):
        value = json.loads(
            self.rfile.read(int(self.headers.get("Content-Length", "0")))
        )
        with self.lock:
            type(self).calls += 1
        model = value.get("model")
        content = "test response"
        usage = {"prompt_tokens": 5, "completion_tokens": 3, "total_tokens": 8}
        response = {
            "id": "chatcmpl-test",
            "object": "chat.completion",
            "created": int(time.time()),
            "model": model,
            "choices": [
                {
                    "index": 0,
                    "message": {"role": "assistant", "content": content},
                    "finish_reason": "stop",
                }
            ],
            "usage": usage,
        }
        if self.path.endswith("/embeddings"):
            inputs = value.get("input", [])
            if isinstance(inputs, str):
                inputs = [inputs]
            response = {
                "object": "list",
                "model": model,
                "data": [
                    {"object": "embedding", "index": i, "embedding": [0.1, 0.2]}
                    for i, _ in enumerate(inputs)
                ],
                "usage": {"prompt_tokens": 5, "total_tokens": 5},
            }
        self.send_response(200)
        if value.get("stream"):
            self.send_header("Content-Type", "text/event-stream")
            self.end_headers()
            chunk = {
                "id": "chatcmpl-test",
                "object": "chat.completion.chunk",
                "created": int(time.time()),
                "model": model,
                "choices": [
                    {
                        "index": 0,
                        "delta": {"role": "assistant", "content": content},
                        "finish_reason": "stop",
                    }
                ],
                "usage": usage,
            }
            try:
                self.wfile.write(
                    ("data: " + json.dumps(chunk) + "\n\ndata: [DONE]\n\n").encode()
                )
            except (BrokenPipeError, ConnectionResetError):
                pass
        else:
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(json.dumps(response).encode())


class Run:
    def __init__(self):
        self.work = Path(tempfile.mkdtemp(prefix="ai-gateway-identity-"))
        os.chmod(self.work, 0o700)
        self.docker = ["docker"]
        if sys.platform == "darwin":
            self.command(["rdctl", "api", "/v1/backend_state"])
            self.docker += ["--context", "rancher-desktop"]
        self.env = {
            k: v
            for k, v in os.environ.items()
            if not k.startswith(
                (
                    "AUTH_",
                    "BILLING_",
                    "PROVIDER_",
                    "MODEL_",
                    "ADMIN_SSO_",
                    "DLP_",
                    "AV_",
                    "ANONYMIZER_",
                    "REDIS_",
                    "MANAGEMENT_",
                )
            )
        }
        self.env["GOTOOLCHAIN"] = os.getenv("GOTOOLCHAIN", "auto")
        self.processes, self.containers, self.logs = [], [], []
        self.secret = secrets.token_urlsafe(32)
        self.password = secrets.token_urlsafe(32)
        self.client_secret = secrets.token_urlsafe(32)
        self.kc_port, self.gw_port, self.ui_port = free_port(), free_port(), free_port()
        self.kc = "http://127.0.0.1:" + str(self.kc_port)
        self.issuer = self.kc + "/realms/gateway-users"
        self.gateway = "http://127.0.0.1:" + str(self.gw_port)
        self.suffix = secrets.token_hex(5)
        self.provider = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Provider)
        self.provider_thread = threading.Thread(
            target=self.provider.serve_forever, daemon=True
        )
        self.provider_thread.start()
        self.auth_dsn = os.environ["AUTH_POSTGRES_TEST_DSN"]
        self.billing_dsn = os.environ["BILLING_POSTGRES_TEST_DSN"]
        self.gateway_admin_dsn = os.environ["CONTROL_PLANE_POSTGRES_TEST_DSN"]
        parsed = urllib.parse.urlsplit(self.gateway_admin_dsn)
        self.gateway_database = "identity_gateway_" + self.suffix
        self.gateway_dsn = urllib.parse.urlunsplit(
            parsed._replace(path="/" + self.gateway_database)
        )

    @staticmethod
    def command(args, **kwargs):
        result = subprocess.run(
            args, stdout=subprocess.PIPE, stderr=subprocess.PIPE, **kwargs
        )
        # Do not render arguments/stderr: commands may include test secrets.
        check(result.returncode == 0, "test command failed: " + Path(args[0]).name)
        return result.stdout

    def sql(self, dsn, statement):
        return (
            self.command(
                ["psql", dsn, "-v", "ON_ERROR_STOP=1", "-At"], input=statement.encode()
            )
            .decode()
            .strip()
        )

    def launch(self, service, settings):
        binary = self.work / service
        self.command(
            ["go", "build", "-o", str(binary), "./cmd/" + service],
            cwd=ROOT / "repos" / service,
            env=self.env,
        )
        log = open(self.work / (service + ".log"), "wb")
        os.chmod(log.name, 0o600)
        self.logs.append(log)
        process = subprocess.Popen(
            [str(binary)], env=dict(self.env, **settings), stdout=log, stderr=log
        )
        self.processes.append(process)
        return process

    def container(self, name, image, options, command=()):
        args = (
            self.docker
            + ["run", "-d", "--name", name]
            + options
            + [image]
            + list(command)
        )
        self.command(args)
        self.containers.append(name)

    def setup(self):
        for port in (8082, 8083):
            with socket.socket() as sock:
                sock.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
                sock.bind(("127.0.0.1", port))
        self.sql(
            self.gateway_admin_dsn, "CREATE DATABASE " + self.gateway_database + ";"
        )
        # Database mutation is restricted to explicitly supplied disposable DSNs.
        for module, dsn in (("auth", self.auth_dsn), ("billing", self.billing_dsn)):
            for migration in sorted(
                (ROOT / "repos" / module / "migrations" / "postgres").glob("*.sql")
            ):
                self.sql(dsn, migration.read_text())
        env = dict(self.env, CONTROL_PLANE_POSTGRES_TEST_DSN=self.gateway_dsn)
        rendered = self.command(
            [
                "helm",
                "template",
                "identity-tests",
                str(ROOT / "charts" / "postgres"),
                "--show-only",
                "templates/migrations-configmap.yaml",
            ]
        )
        self.command(
            ["python3", str(ROOT / "scripts" / "apply-postgres-chart-migrations.py")],
            input=rendered,
            env=env,
        )
        realm = json.loads(
            (ROOT / "examples" / "identity" / "keycloak-realm.json").read_text()
        )
        realm["sslRequired"] = "none"  # Isolated loopback test only.
        realm["accessTokenLifespan"] = 45
        role_client = copy.deepcopy(realm["clients"][1])
        role_client.update(
            clientId="gateway-integration",
            secret=self.client_secret,
            directAccessGrantsEnabled=True,
        )
        realm["clients"].append(role_client)
        realm["clients"][1].update(
            secret=self.client_secret,
            redirectUris=[
                "http://127.0.0.1:" + str(self.ui_port) + "/oauth/oidc/callback"
            ],
            webOrigins=[],
        )
        realm["clientScopeMappings"]["gateway"].append(
            {"client": "gateway-integration", "roles": ["gateway-user"]}
        )
        realm["users"] = [
            {
                "username": name,
                "enabled": True,
                "email": name + "@example.test",
                "emailVerified": True,
                "firstName": name,
                "lastName": "Integration",
                "credentials": [
                    {"type": "password", "value": self.password, "temporary": False}
                ],
                "clientRoles": {"gateway": ["gateway-user"]},
            }
            for name in ("alice", "bob")
        ]
        self.kc_name = "ai-gateway-identity-kc-" + self.suffix
        self.container(
            self.kc_name,
            KEYCLOAK_IMAGE,
            [
                "-p",
                "127.0.0.1:%d:%d" % (self.kc_port, self.kc_port),
                "-p",
                "127.0.0.1:%d:%d" % (self.ui_port, self.ui_port),
                "-e",
                "KC_HTTP_PORT=" + str(self.kc_port),
                "-e",
                "KC_BOOTSTRAP_ADMIN_USERNAME=integration-admin",
                "-e",
                "KC_BOOTSTRAP_ADMIN_PASSWORD=" + self.password,
            ],
            ("start-dev", "--hostname", self.kc),
        )
        wait_ready(self.kc + "/realms/master/.well-known/openid-configuration")
        status, data, _ = request_http(
            self.kc + "/realms/master/protocol/openid-connect/token",
            urllib.parse.urlencode(
                {
                    "grant_type": "password",
                    "client_id": "admin-cli",
                    "username": "integration-admin",
                    "password": self.password,
                }
            ).encode(),
            {"Content-Type": "application/x-www-form-urlencoded"},
        )
        check(status == 200, "test Keycloak bootstrap authorization failed")
        self.admin_token = json.loads(data)["access_token"]
        status, _, _ = request_http(
            self.kc + "/admin/realms",
            realm,
            {"Authorization": "Bearer " + self.admin_token},
        )
        check(status == 201, "test realm creation failed")
        wait_ready(self.issuer + "/.well-known/openid-configuration")
        auth_settings = {
            "AUTH_POSTGRES_KEYS_ENABLED": "true",
            "AUTH_POSTGRES_DSN": self.auth_dsn,
            "AUTH_KEY_HASH_SECRET": self.secret,
            "AUTH_STATIC_KEY_FALLBACK_ENABLED": "false",
            "AUTH_DEMO_KEYS_ENABLED": "false",
            "AUTH_JWT_IDENTITY_MODE": "directory",
            "AUTH_JWT_ISSUER": self.issuer,
            "AUTH_JWT_AUDIENCE": "gateway",
            "AUTH_JWT_JWKS_URL": self.issuer + "/protocol/openid-connect/certs",
            "AUTH_JWT_JWKS_CACHE_TTL_SECONDS": "1",
            "AUTH_JWT_CLOCK_SKEW_SECONDS": "0",
            "AUTH_JWT_ROLES_CLAIM": "resource_access.gateway.roles",
            "AUTH_JWT_ROLE_MAPPINGS_JSON": json.dumps(
                {"gateway-user": "user", "gateway-admin": "admin"}
            ),
            "MANAGEMENT_SHARED_SECRET": self.secret,
        }
        self.launch("auth", auth_settings)
        wait_ready("http://127.0.0.1:8082/readyz")
        self.launch(
            "billing",
            {
                "POSTGRES_DSN": self.billing_dsn,
                "BILLING_DURABLE_OUTBOX_ENABLED": "true",
                "BILLING_LIMITS_ENABLED": "true",
                "BILLING_OUTPUT_PRICE_PER_1K": "1",
                "BILLING_INPUT_PRICE_PER_1K": "1",
                "BILLING_SHARED_SECRET": self.secret,
                "BILLING_MANAGEMENT_SHARED_SECRET": self.secret,
                "BILLING_DEFAULT_RESERVE_OUTPUT_TOKENS": "16",
            },
        )
        wait_ready("http://127.0.0.1:8083/healthz")
        self.launch(
            "gateway",
            {
                "HTTP_ADDR": "0.0.0.0:" + str(self.gw_port),
                "AUTH_REQUIRED": "true",
                "AUTH_URL": "http://127.0.0.1:8082",
                "BILLING_REQUIRED": "true",
                "BILLING_URL": "http://127.0.0.1:8083",
                "BILLING_SHARED_SECRET": self.secret,
                "MANAGEMENT_SHARED_SECRET": self.secret,
                "MANAGEMENT_BILLING_URL": "http://127.0.0.1:8083",
                "BILLING_MANAGEMENT_SHARED_SECRET": self.secret,
                "DLP_REQUIRED": "false",
                "AV_REQUIRED": "false",
                "ADMIN_UI_ENABLED": "false",
                "EXACT_CACHE_TTL_SECONDS": "120",
                "PROVIDER_CREDENTIAL_ENCRYPTION_KEY": self.secret,
                "PROVIDER_CONTROL_PLANE_POSTGRES_DSN": self.gateway_dsn,
                "PROVIDERS_JSON": json.dumps(
                    [
                        {
                            "name": "identity-fixture",
                            "type": "openai-compatible",
                            "base_url": "http://127.0.0.1:"
                            + str(self.provider.server_port)
                            + "/v1",
                            "models": ["model-a", "model-b", "model-common"],
                            "enabled": True,
                        }
                    ]
                ),
            },
        )
        wait_ready(self.gateway + "/readyz")
        self.tokens, self.policies, self.identities = {}, {}, {}
        for name, model in (("alice", "model-a"), ("bob", "model-b")):
            token = self.token(name)
            self.tokens[name] = token
            user = "identity-" + self.suffix + "-" + name
            self.internal(
                "PUT",
                "/users/" + user,
                {"status": "active", "roles": ["user"], "name": name},
            )
            policy = {
                "issuer": self.issuer,
                "subject": token_claims(token["access_token"])["sub"],
                "audience": "gateway",
                "user_id": user,
                "enabled": True,
                "allowed_models": [model, "model-common"],
                "allowed_tools": ["read"],
                "rate_limit_rpm": 100,
                "rate_limit_tpm": 100000,
            }
            self.internal("PUT", "/jwt-principals", policy)
            self.policies[name] = policy
            status, data, _ = self.authorize(token["access_token"])
            check(status == 200, "real Keycloak token authorization failed")
            self.identities[name] = json.loads(data)
        print(
            "PASS real realm, user provisioning and RS256/JWKS authorization",
            flush=True,
        )

    def internal(self, method, path, body):
        status, data, _ = request_http(
            "http://127.0.0.1:8082/internal/v1" + path,
            body,
            method=method,
            headers={
                "X-Management-Token": self.secret,
                "X-Request-ID": "integration",
                "X-Actor-ID": "test-operator",
                "X-Actor-Credential-ID": "integration",
            },
        )
        check(status == 200, "internal test provisioning failed")
        return json.loads(data)

    def token(self, name=None, refresh=None):
        fields = {
            "client_id": "gateway-integration",
            "client_secret": self.client_secret,
        }
        if refresh:
            fields.update(grant_type="refresh_token", refresh_token=refresh)
        else:
            fields.update(
                grant_type="password",
                username=name,
                password=self.password,
                scope="openid email profile",
            )
        status, data, _ = request_http(
            self.issuer + "/protocol/openid-connect/token",
            urllib.parse.urlencode(fields).encode(),
            {"Content-Type": "application/x-www-form-urlencoded"},
        )
        check(status == 200, "Keycloak test token grant failed")
        return json.loads(data)

    @staticmethod
    def authorize(token):
        return request_http("http://127.0.0.1:8082/authorize", {"token": token})

    def api(self, name, path, body=None, method=None, extra=None):
        headers = {"Authorization": "Bearer " + self.tokens[name]["access_token"]}
        headers.update(extra or {})
        return request_http(self.gateway + path, body, headers, method)

    def gateway_checks(self):
        for name, allowed, hidden in (
            ("alice", "model-a", "model-b"),
            ("bob", "model-b", "model-a"),
        ):
            status, data, _ = self.api(name, "/v1/models")
            check(
                status == 200
                and {m["id"] for m in json.loads(data)["data"]}
                == {allowed, "model-common"},
                "per-user model list mismatch",
            )
            request = {
                "model": hidden,
                "messages": [{"role": "user", "content": "hello"}],
            }
            status, _, _ = self.api(
                name,
                "/v1/chat/completions",
                request,
                extra={"X-OpenWebUI-User-Id": self.policies["alice"]["user_id"]},
            )
            check(status == 403, "hidden model/header spoof bypassed Gateway")
        request = {
            "model": "model-common",
            "messages": [{"role": "user", "content": "hello"}],
        }
        status, data, _ = self.api("alice", "/v1/chat/completions", request)
        code = (
            json.loads(data).get("error", {}).get("code", "") if status != 200 else ""
        )
        check(
            status == 200,
            "JSON inference failed: status="
            + str(status)
            + " code="
            + code
            + " reason="
            + (
                re.sub(
                    r"[a-zA-Z0-9_-]{24,}|https?://[^ ]+",
                    "[redacted]",
                    json.loads(data).get("error", {}).get("message", ""),
                )
                if status != 200
                else ""
            ),
        )
        first_calls = Provider.calls
        refreshed = self.token(refresh=self.tokens["alice"]["refresh_token"])
        status, data, _ = self.authorize(refreshed["access_token"])
        identity = json.loads(data)
        check(
            status == 200
            and identity["credential_id"] == self.identities["alice"]["credential_id"]
            and identity["user_id"] == self.identities["alice"]["user_id"],
            "refresh changed stable identity",
        )
        self.tokens["alice"] = refreshed
        status, _, _ = self.api("alice", "/v1/chat/completions", request)
        check(
            status == 200 and Provider.calls == first_calls,
            "refresh changed exact cache scope",
        )
        status, _, _ = self.api("bob", "/v1/chat/completions", request)
        check(
            status == 200 and Provider.calls == first_calls + 1,
            "cross-user cache reuse",
        )
        request["stream"] = True
        status, data, _ = self.api("alice", "/v1/chat/completions", request)
        check(
            status == 200 and b"[DONE]" in data and b"test response" in data,
            "SSE failed",
        )
        request.pop("stream")
        request["tools"] = [
            {
                "type": "function",
                "function": {
                    "name": "write",
                    "parameters": {"type": "object", "properties": {}},
                },
            }
        ]
        status, _, _ = self.api("alice", "/v1/chat/completions", request)
        check(status == 403, "unassigned tool allowed")
        request["tools"][0]["function"]["name"] = "read"
        status, _, _ = self.api("alice", "/v1/chat/completions", request)
        check(status == 200, "assigned tool denied")
        status, data, _ = self.api(
            "alice", "/v1/conversations", {"metadata": {"test": self.suffix}}
        )
        check(status == 200, "conversation creation failed")
        conversation = json.loads(data)["id"]
        self.tokens["alice"] = self.token(refresh=self.tokens["alice"]["refresh_token"])
        check(
            self.api("alice", "/v1/conversations/" + conversation)[0] == 200,
            "conversation ownership changed after refresh",
        )
        check(
            self.api("bob", "/v1/conversations/" + conversation)[0] == 404,
            "cross-user conversation access",
        )
        boundary = "identity-test-boundary"
        payload = (
            "--"
            + boundary
            + '\r\nContent-Disposition: form-data; name="purpose"\r\n\r\nuser_data\r\n--'
            + boundary
            + '\r\nContent-Disposition: form-data; name="file"; filename="test.txt"\r\nContent-Type: text/plain\r\n\r\nfixture\r\n--'
            + boundary
            + "--\r\n"
        ).encode()
        status, data, _ = self.api(
            "alice",
            "/v1/files",
            payload,
            extra={"Content-Type": "multipart/form-data; boundary=" + boundary},
        )
        check(status == 200, "file upload failed")
        file_id = json.loads(data)["id"]
        self.tokens["alice"] = self.token(refresh=self.tokens["alice"]["refresh_token"])
        check(
            self.api("alice", "/v1/files/" + file_id)[0] == 200,
            "file ownership changed after refresh",
        )
        check(
            self.api("bob", "/v1/files/" + file_id)[0] == 404, "cross-user file access"
        )
        history = {
            "model": "model-common",
            "tools": [
                {
                    "type": "function",
                    "function": {
                        "name": "read",
                        "parameters": {"type": "object", "properties": {}},
                    },
                }
            ],
            "messages": [
                {
                    "role": "assistant",
                    "tool_calls": [
                        {
                            "id": "call_read",
                            "type": "function",
                            "function": {"name": "read", "arguments": "{}"},
                        }
                    ],
                },
                {
                    "role": "tool",
                    "tool_call_id": "call_read",
                    "content": "fixture result",
                },
            ],
        }
        check(
            self.api("alice", "/v1/chat/completions", history)[0] == 200,
            "authorized tool-result history failed",
        )
        check(
            self.api(
                "alice", "/v1/embeddings", {"model": "model-common", "input": "fixture"}
            )[0]
            == 200,
            "authorized embeddings failed",
        )
        with concurrent.futures.ThreadPoolExecutor(max_workers=4) as executor:
            statuses = list(
                executor.map(
                    lambda name: self.api(
                        name,
                        "/v1/chat/completions",
                        {
                            "model": "model-common",
                            "messages": [{"role": "user", "content": "load fixture"}],
                        },
                    )[0],
                    ["alice", "bob"] * 6,
                )
            )
        check(
            all(status == 200 for status in statuses),
            "concurrent identity traffic failed",
        )
        check(
            self.api("alice", "/admin/v1/users")[0] == 403,
            "inference role escalated to admin",
        )
        check(self.authorize("not-a-jwt")[0] == 401, "local session token accepted")
        self.internal(
            "PUT",
            "/users/" + self.policies["alice"]["user_id"],
            {"status": "disabled", "roles": ["user"]},
        )
        check(
            self.api("alice", "/v1/models")[0] == 401,
            "disabled user retained inference access",
        )
        self.internal(
            "PUT",
            "/users/" + self.policies["alice"]["user_id"],
            {"status": "active", "roles": ["user"]},
        )
        check(self.api("alice", "/v1/models")[0] == 200, "directory recovery failed")
        # Refreshed executions share one stable credential and user attribution.
        user = self.policies["alice"]["user_id"]
        result = self.sql(
            self.billing_dsn,
            "SELECT count(DISTINCT credential_id),count(*),sum(actual_tokens) FROM billing_budget_reservations WHERE user_id='"
            + user
            + "' AND state='committed';",
        )
        columns = result.split("|")
        check(
            columns[0] == "1" and int(columns[1]) >= 3 and int(columns[2]) > 0,
            "billing identity split or missing committed usage",
        )
        limited = copy.deepcopy(self.policies["bob"])
        limited["rate_limit_rpm"] = 1
        self.internal("PUT", "/jwt-principals", limited)
        request = {
            "model": "model-b",
            "messages": [{"role": "user", "content": "quota fixture"}],
        }
        check(
            self.api("bob", "/v1/chat/completions", request)[0] == 429,
            "refresh reset shared RPM counter",
        )
        self.internal("PUT", "/jwt-principals", self.policies["bob"])
        self.sql(
            self.billing_dsn,
            "INSERT INTO billing_budget_policies(scope_type,scope_id,period,currency,max_tokens) VALUES('user','"
            + user
            + "','day','USD',1);",
        )
        request["model"] = "model-a"
        check(
            self.api("alice", "/v1/chat/completions", request)[0] == 429,
            "user budget not enforced after refresh",
        )
        self.sql(
            self.billing_dsn,
            "UPDATE billing_budget_policies SET enabled=false WHERE scope_type='user' AND scope_id='"
            + user
            + "';",
        )
        print(
            "PASS models/tools, JSON/SSE, cache isolation, refresh ownership, deprovision/recovery and PostgreSQL attribution",
            flush=True,
        )

    def keycloak_checks(self):
        check(
            self.authorize(self.tokens["alice"]["id_token"])[0] == 401,
            "Keycloak ID token accepted for inference",
        )
        status, data, _ = request_http(
            self.kc + "/realms/master/protocol/openid-connect/token",
            urllib.parse.urlencode(
                {
                    "grant_type": "password",
                    "client_id": "admin-cli",
                    "username": "integration-admin",
                    "password": self.password,
                }
            ).encode(),
            {"Content-Type": "application/x-www-form-urlencoded"},
        )
        check(status == 200, "Keycloak test admin refresh failed")
        admin_headers = {"Authorization": "Bearer " + json.loads(data)["access_token"]}
        status, data, _ = request_http(
            self.kc + "/admin/realms/gateway-users", headers=admin_headers
        )
        check(status == 200, "Keycloak test realm inspection failed")
        realm_id = json.loads(data)["id"]
        old_token = self.tokens["alice"]["access_token"]
        kid = lambda token: json.loads(
            base64.urlsafe_b64decode(
                token.split(".")[0] + "=" * (-len(token.split(".")[0]) % 4)
            )
        )["kid"]
        component = {
            "name": "integration-rotation",
            "parentId": realm_id,
            "providerId": "rsa-generated",
            "providerType": "org.keycloak.keys.KeyProvider",
            "config": {
                "priority": ["200"],
                "enabled": ["true"],
                "active": ["true"],
                "algorithm": ["RS256"],
                "keySize": ["2048"],
            },
        }
        status, _, _ = request_http(
            self.kc + "/admin/realms/gateway-users/components", component, admin_headers
        )
        check(status == 201, "Keycloak signing-key rotation failed")
        rotated = self.token("alice")
        check(
            kid(rotated["access_token"]) != kid(old_token),
            "Keycloak did not activate rotated signing key",
        )
        deadline = time.monotonic() + 10
        while (
            self.authorize(rotated["access_token"])[0] != 200
            and time.monotonic() < deadline
        ):
            time.sleep(0.25)
        status, data, _ = self.authorize(rotated["access_token"])
        check(
            status == 200
            and json.loads(data)["credential_id"]
            == self.identities["alice"]["credential_id"],
            "JWKS rotation changed or lost principal",
        )
        self.tokens["alice"] = rotated
        subject = self.policies["alice"]["subject"]
        status, _, _ = request_http(
            self.kc + "/admin/realms/gateway-users/users/" + subject,
            {"enabled": False},
            admin_headers,
            method="PUT",
        )
        check(status == 204, "Keycloak user disable failed")
        self.internal(
            "PUT",
            "/users/" + self.policies["alice"]["user_id"],
            {"status": "disabled", "roles": ["user"]},
        )
        check(
            self.api("alice", "/v1/models")[0] == 401,
            "IdP/directory deprovision did not revoke signed token",
        )
        status, _, _ = request_http(
            self.kc + "/admin/realms/gateway-users/users/" + subject,
            {"enabled": True},
            admin_headers,
            method="PUT",
        )
        check(status == 204, "Keycloak user recovery failed")
        self.internal(
            "PUT",
            "/users/" + self.policies["alice"]["user_id"],
            {"status": "active", "roles": ["user"]},
        )
        self.tokens["alice"] = self.token("alice")
        # Realm logout ends IdP sessions; immediate directory disable is separate.
        status, _, _ = request_http(
            self.kc + "/admin/realms/gateway-users/users/" + subject + "/logout",
            b"",
            admin_headers,
            method="POST",
        )
        check(status == 204, "Keycloak session logout failed")
        fields = {
            "grant_type": "refresh_token",
            "client_id": "gateway-integration",
            "client_secret": self.client_secret,
            "refresh_token": self.tokens["alice"]["refresh_token"],
        }
        status, _, _ = request_http(
            self.issuer + "/protocol/openid-connect/token",
            urllib.parse.urlencode(fields).encode(),
            {"Content-Type": "application/x-www-form-urlencoded"},
        )
        check(status == 400, "logout did not invalidate refresh session")
        self.tokens["alice"] = self.token("alice")
        # Expiry must be enforced even while a browser/local session remains valid.
        status, data, _ = request_http(
            self.kc
            + "/admin/realms/gateway-users/clients?clientId=gateway-integration",
            headers=admin_headers,
        )
        check(status == 200, "test client lookup failed")
        client = json.loads(data)[0]
        client.setdefault("attributes", {})["access.token.lifespan"] = "1"
        client_url = self.kc + "/admin/realms/gateway-users/clients/" + client["id"]
        check(
            request_http(client_url, client, admin_headers, "PUT")[0] == 204,
            "short-lived test token configuration failed",
        )
        try:
            expired = self.token("alice")["access_token"]
            deadline = time.monotonic() + 5
            while self.authorize(expired)[0] == 200 and time.monotonic() < deadline:
                time.sleep(0.1)
            check(self.authorize(expired)[0] == 401, "expired access token accepted")
        finally:
            client["attributes"]["access.token.lifespan"] = "45"
            check(
                request_http(client_url, client, admin_headers, "PUT")[0] == 204,
                "test token lifetime restore failed",
            )
        self.tokens["alice"] = self.token("alice")
        self.tokens["bob"] = self.token("bob")
        print(
            "PASS Keycloak ID-token/expiry rejection, real signing-key rotation, deprovision/recovery and logout/refresh revocation",
            flush=True,
        )

    def failure_checks(self):
        # Only the explicitly provided disposable directory database is changed.
        # An unavailable binding table exercises a real SQL failure, not a fake.
        self.sql(
            self.auth_dsn,
            "ALTER TABLE auth_jwt_principals RENAME TO identity_unavailable_bindings;",
        )
        try:
            check(
                self.api("alice", "/v1/models")[0] >= 500,
                "directory outage did not fail closed",
            )
        finally:
            self.sql(
                self.auth_dsn,
                "ALTER TABLE identity_unavailable_bindings RENAME TO auth_jwt_principals;",
            )
        check(
            self.api("alice", "/v1/models")[0] == 200,
            "directory outage recovery failed",
        )
        # Stop only this run's realm. The one-second JWKS cache expires
        # during shutdown; validation must fail closed until keys can reload.
        self.command(self.docker + ["stop", "-t", "10", self.kc_name])
        try:
            try:
                self.token("alice")
                raise RuntimeError("unavailable IdP issued a token")
            except (OSError, urllib.error.URLError):
                pass
            deadline = time.monotonic() + 4
            status = self.api("alice", "/v1/models")[0]
            while status == 200 and time.monotonic() < deadline:
                time.sleep(0.1)
                status = self.api("alice", "/v1/models")[0]
            check(
                status in (401, 502, 503),
                "expired JWKS cache admitted access during IdP outage: status="
                + str(status),
            )
        finally:
            self.command(self.docker + ["start", self.kc_name])
            wait_ready(self.issuer + "/.well-known/openid-configuration")
        self.tokens["alice"] = self.token("alice")
        self.tokens["bob"] = self.token("bob")
        check(
            self.api("alice", "/v1/models")[0] == 200,
            "IdP restart lost provisioned principal",
        )
        print(
            "PASS directory SQL failure/recovery and IdP unavailability/restart with bounded JWT trust",
            flush=True,
        )

    def openwebui_checks(self):
        host = (
            "host.docker.internal"
            if sys.platform == "darwin"
            else self.command(
                self.docker
                + [
                    "network",
                    "inspect",
                    "bridge",
                    "--format",
                    "{{(index .IPAM.Config 0).Gateway}}",
                ]
            )
            .decode()
            .strip()
        )
        self.ui = "http://127.0.0.1:" + str(self.ui_port)
        settings = {
            "PORT": str(self.ui_port),
            "WEBUI_URL": self.ui,
            "WEBUI_SECRET_KEY": self.secret,
            "OAUTH_SESSION_TOKEN_ENCRYPTION_KEY": base64.urlsafe_b64encode(
                secrets.token_bytes(32)
            ).decode(),
            "OAUTH_CLIENT_ID": "openwebui",
            "OAUTH_CLIENT_SECRET": self.client_secret,
            "OPENID_PROVIDER_URL": self.issuer + "/.well-known/openid-configuration",
            "OAUTH_PROVIDER_NAME": "Keycloak",
            "OAUTH_SCOPES": "openid email profile",
            "OAUTH_CODE_CHALLENGE_METHOD": "S256",
            "ENABLE_OAUTH_SIGNUP": "true",
            "ENABLE_SIGNUP": "true",
            "DEFAULT_USER_ROLE": "user",
            "OPENAI_API_BASE_URLS": "http://" + host + ":" + str(self.gw_port) + "/v1",
            "OPENAI_API_KEYS": "",
            "ENABLE_OLLAMA_API": "false",
            "ENABLE_PERSISTENT_CONFIG": "false",
            "BYPASS_MODEL_ACCESS_CONTROL": "true",
            "OFFLINE_MODE": "true",
            "HF_HUB_OFFLINE": "1",
            "RAG_EMBEDDING_ENGINE": "openai",
            "RAG_EMBEDDING_MODEL": "model-common",
            "MODELS_CACHE_TTL": "60",
            "GLOBAL_LOG_LEVEL": "ERROR",
            "WEBUI_AUTH_COOKIE_SECURE": "false",
            "AUTHLIB_INSECURE_TRANSPORT": "1",
        }
        options = ["--network", "container:" + self.kc_name]
        for key, value in settings.items():
            options += ["-e", key + "=" + value]
        self.ui_name = "ai-gateway-identity-ui-" + self.suffix
        self.container(self.ui_name, OPENWEBUI_IMAGE, options)
        wait_ready(self.ui + "/health", seconds=240)
        sessions = {}
        for name in ("alice", "bob"):
            jar = http.cookiejar.CookieJar(policy=LoopbackCookiePolicy())
            opener = urllib.request.build_opener(
                urllib.request.HTTPCookieProcessor(jar)
            )
            status, page, _ = request_http(self.ui + "/oauth/oidc/login", opener=opener)
            check(status == 200, "OpenWebUI OIDC redirect failed")
            form = re.search(
                r'<form[^>]*id="kc-form-login"[^>]*action="([^"]+)"', page.decode()
            )
            check(form is not None, "Keycloak browser login form missing")
            action = html.unescape(form.group(1))
            if os.getenv("IDENTITY_COOKIE_DIAGNOSTICS") == "true":
                print(
                    "Keycloak login action:",
                    urllib.parse.urlsplit(action).path,
                    flush=True,
                )
                for c in jar:
                    print(
                        "Cookie scope:",
                        c.name,
                        c.domain,
                        c.path,
                        "secure=" + str(c.secure),
                        flush=True,
                    )
            status, callback_data, _ = request_http(
                action,
                urllib.parse.urlencode(
                    {"username": name, "password": self.password, "credentialId": ""}
                ).encode(),
                {"Content-Type": "application/x-www-form-urlencoded"},
                opener=opener,
            )
            if status != 200:
                page_text = callback_data.decode(errors="replace")
                match = re.search(
                    r'class="kc-feedback-text"[^>]*>(.*?)</span>', page_text, re.S
                )
                reason = (
                    html.unescape(re.sub(r"<[^>]+>", "", match.group(1)))
                    if match
                    else "No Keycloak feedback: "
                    + re.sub(r"<[^>]+>", "", page_text)[-350:]
                )
                reason = re.sub(
                    r"[a-zA-Z0-9_-]{24,}|https?://[^ \"<]+", "[redacted]", reason
                )
                raise RuntimeError(
                    "OpenWebUI callback status=" + str(status) + " reason=" + reason
                )
            cookies = {c.name: c.value for c in jar}
            check(
                cookies.get("oauth_session_id") and cookies.get("token"),
                "server OAuth session was not created; cookie names="
                + ",".join(sorted(cookies)),
            )
            sessions[name] = (opener, jar)
            if name == "alice":
                # Pinned version does not read OPENAI_API_CONFIGS from environment.
                status, _, _ = request_http(
                    self.ui + "/openai/config/update",
                    {
                        "ENABLE_OPENAI_API": True,
                        "OPENAI_API_BASE_URLS": [settings["OPENAI_API_BASE_URLS"]],
                        "OPENAI_API_KEYS": [""],
                        "OPENAI_API_CONFIGS": {
                            "0": {"enable": True, "auth_type": "system_oauth"}
                        },
                    },
                    opener=opener,
                )
                check(status == 200, "System OAuth connection configuration failed")
        for name, model, hidden in (
            ("alice", "model-a", "model-b"),
            ("bob", "model-b", "model-a"),
        ):
            opener, jar = sessions[name]
            for _ in range(2):
                status, data, _ = request_http(
                    self.ui + "/openai/models", opener=opener
                )
                check(
                    status == 200
                    and {m["id"] for m in json.loads(data)["data"]}
                    == {model, "model-common"},
                    "OpenWebUI per-user model cache mismatch: user="
                    + name
                    + " status="
                    + str(status)
                    + " model_ids="
                    + ",".join(m["id"] for m in json.loads(data).get("data", [])),
                )
            request = {
                "model": model,
                "messages": [{"role": "user", "content": "UI fixture"}],
            }
            status, _, _ = request_http(
                self.ui + "/openai/chat/completions", request, opener=opener
            )
            check(status == 200, "OpenWebUI System OAuth JSON failed")
            request["stream"] = True
            status, data, _ = request_http(
                self.ui + "/openai/chat/completions", request, opener=opener
            )
            check(
                status == 200 and b"[DONE]" in data, "OpenWebUI System OAuth SSE failed"
            )
            request["stream"] = False
            request["model"] = hidden
            status, _, _ = request_http(
                self.ui + "/openai/chat/completions", request, opener=opener
            )
            check(
                status in (400, 403, 404),
                "OpenWebUI hidden model bypassed Gateway enforcement",
            )
            request["model"] = model
            user = self.policies[name]["user_id"]
            self.internal(
                "PUT", "/users/" + user, {"status": "disabled", "roles": ["user"]}
            )
            try:
                check(
                    request_http(
                        self.ui + "/openai/chat/completions", request, opener=opener
                    )[0]
                    == 401,
                    "OpenWebUI concealed directory revocation",
                )
            finally:
                self.internal(
                    "PUT", "/users/" + user, {"status": "active", "roles": ["user"]}
                )
            self.sql(
                self.billing_dsn,
                "INSERT INTO billing_budget_policies(scope_type,scope_id,period,currency,max_tokens) VALUES('user','"
                + user
                + "','day','USD',1);",
            )
            try:
                check(
                    request_http(
                        self.ui + "/openai/chat/completions", request, opener=opener
                    )[0]
                    == 429,
                    "OpenWebUI concealed budget exhaustion",
                )
            finally:
                self.sql(
                    self.billing_dsn,
                    "UPDATE billing_budget_policies SET enabled=false WHERE scope_type='user' AND scope_id='"
                    + user
                    + "';",
                )
            # Losing only the OAuth session must not use a connection-wide key.
            for cookie in list(jar):
                if cookie.name == "oauth_session_id":
                    jar.clear(cookie.domain, cookie.path, cookie.name)
            request["model"] = model
            status, _, _ = request_http(
                self.ui + "/openai/chat/completions", request, opener=opener
            )
            check(
                status == 401,
                "missing OAuth session used a shared/local token fallback",
            )
        for name in ("alice", "bob"):
            user = self.policies[name]["user_id"]
            count = self.sql(
                self.billing_dsn,
                "SELECT count(DISTINCT credential_id) FROM billing_budget_reservations WHERE user_id='"
                + user
                + "' AND state='committed';",
            )
            check(count == "1", "OpenWebUI refresh split billing identity")
        print(
            "PASS OpenWebUI v0.11.4 real authorization-code/PKCE, System OAuth, per-user model cache, JSON/SSE, refresh billing and 401/429/missing-session denial",
            flush=True,
        )
        if os.getenv("IDENTITY_BROWSER_READY_FILE"):
            ready = Path(os.environ["IDENTITY_BROWSER_READY_FILE"])
            ready.write_text(
                json.dumps(
                    {
                        "url": self.ui,
                        "password": self.password,
                        "keycloak": self.kc,
                        "users": ["alice", "bob"],
                        "directory_users": {
                            name: policy["user_id"]
                            for name, policy in self.policies.items()
                        },
                        "auth_test_dsn": self.auth_dsn,
                        "billing_test_dsn": self.billing_dsn,
                    }
                )
            )
            os.chmod(ready, 0o600)
            print("Browser OAuth diagnostic services ready", flush=True)
            deadline = time.monotonic() + 300
            while (
                time.monotonic() < deadline and not Path(str(ready) + ".done").exists()
            ):
                time.sleep(1)
            check(Path(str(ready) + ".done").exists(), "browser verification timed out")

    def close(self):
        for process in reversed(self.processes):
            if process.poll() is None:
                process.terminate()
                try:
                    process.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait(timeout=5)
        for name in reversed(self.containers):
            subprocess.run(
                self.docker + ["stop", "-t", "10", name],
                stdout=subprocess.DEVNULL,
                stderr=subprocess.DEVNULL,
            )
        for log in self.logs:
            log.close()
        self.provider.shutdown()
        self.provider.server_close()
        self.provider_thread.join(timeout=5)


def main():
    for key in (
        "AUTH_POSTGRES_TEST_DSN",
        "BILLING_POSTGRES_TEST_DSN",
        "CONTROL_PLANE_POSTGRES_TEST_DSN",
    ):
        check(os.getenv(key), "dedicated test DSN required: " + key)
    check(
        os.getenv("IDENTITY_INTEGRATION_TESTS") == "true",
        "set IDENTITY_INTEGRATION_TESTS=true for disposable integration databases",
    )
    run = Run()
    try:
        run.setup()
        run.gateway_checks()
        run.keycloak_checks()
        run.failure_checks()
        run.openwebui_checks()
    finally:
        run.close()


if __name__ == "__main__":
    try:
        main()
    except Exception as error:
        # URLs/forms/headers in library errors can contain credentials.
        print(
            "FAIL identity integration: "
            + (str(error) if isinstance(error, RuntimeError) else type(error).__name__),
            file=sys.stderr,
        )
        for frame in traceback.extract_tb(error.__traceback__):
            print(
                "  "
                + Path(frame.filename).name
                + ":"
                + str(frame.lineno)
                + " "
                + frame.name,
                file=sys.stderr,
            )
        sys.exit(1)
