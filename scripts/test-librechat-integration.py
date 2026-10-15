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
import json
import os
from pathlib import Path
import re
import secrets
import subprocess
import sys
import traceback
import urllib.parse
import urllib.request
import uuid


ROOT = Path(__file__).resolve().parent.parent
LIBRECHAT_IMAGE = "ghcr.io/librechat-ai/librechat:v0.8.8@sha256:5b706bcee1ca154a1e2987303aaeca0eb27f97919cccee4bb604226c2f8abf61"
MONGO_IMAGE = "mongo:8.0.20@sha256:098862b1339f031900ca66cf8fef799e616d6324fa41b9a263f2ec899552c1ef"
spec = importlib.util.spec_from_file_location("identity_integration", Path(__file__).with_name("test-identity-integration.py"))
identity = importlib.util.module_from_spec(spec)
spec.loader.exec_module(identity)
check = identity.check
request_http = identity.request_http


def profile():
    return json.loads((ROOT / "examples/librechat/librechat.yaml").read_text())


def environment():
    text = (ROOT / "examples/librechat/.env.example").read_text()
    return dict(line.split("=", 1) for line in text.splitlines() if line and not line.startswith("#"))


def existing_images(docker):
    for image in (identity.KEYCLOAK_IMAGE, LIBRECHAT_IMAGE, MONGO_IMAGE):
        result = subprocess.run(docker + ["image", "inspect", image], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        check(result.returncode == 0, "required test image is missing; obtain permission and pull the pinned images explicitly")


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
        # Python, Go and Node trust the same disposable CA. Certificate and
        # hostname verification remain enabled throughout the real OAuth flow.
        self.previous_ca = os.environ.get("SSL_CERT_FILE")
        os.environ["SSL_CERT_FILE"] = str(self.tls / "ca.crt")
        self.env["SSL_CERT_FILE"] = str(self.tls / "ca.crt")

    def container(self, name, image, options, command=()):
        if image != identity.KEYCLOAK_IMAGE:
            return super().container(name, image, options, command)
        options = ["KC_HTTP_PORT=" + str(self.upstream_kc_port) if item.startswith("KC_HTTP_PORT=") else item for item in options]
        options += ["-e", "KC_PROXY_HEADERS=xforwarded"]
        super().container(name, image, options, command)
        super().container("ai-gateway-librechat-tls-" + self.suffix, LIBRECHAT_IMAGE,
                          ["--network", "container:" + name, "--user", "0:0", "--memory", "128m", "--entrypoint", "node",
                           "--mount", "type=bind,src=" + str(self.tls) + ",dst=/fixture,readonly",
                           "--mount", "type=bind,src=" + str(ROOT / "scripts/fixtures/identity-tls-proxy.cjs") + ",dst=/proxy.cjs,readonly",
                           "-e", "FIXTURE_TLS_PORT=" + str(self.kc_port), "-e", "UPSTREAM_HTTP_PORT=" + str(self.upstream_kc_port)],
                          ("/proxy.cjs",))

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
        config_path.write_text(json.dumps(profile()))
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
        opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar))
        status, page, _ = request_http(self.ui + "/oauth/openid", opener=opener)
        form = re.search(r'<form[^>]*id="kc-form-login"[^>]*action="([^"]+)"', page.decode())
        check(status == 200 and form is not None, "LibreChat did not start real Keycloak browser authorization")
        status, _, _ = request_http(html.unescape(form.group(1)), urllib.parse.urlencode({"username": name, "password": self.password, "credentialId": ""}).encode(), {"Content-Type": "application/x-www-form-urlencoded"}, opener=opener)
        check(status == 200, "LibreChat OAuth callback failed")
        status, data, _ = request_http(self.ui + "/api/auth/refresh", {}, {"Origin": self.ui}, opener=opener)
        check(status == 200 and json.loads(data).get("token"), "LibreChat did not establish an authenticated OpenID session")
        return {"opener": opener, "token": json.loads(data)["token"], "user": json.loads(data).get("user", {})}

    def ui_api(self, session, path, body=None, method=None):
        return request_http(self.ui + path, body, {"Authorization": "Bearer " + session["token"], "Origin": self.ui}, method, opener=session["opener"])

    def chat(self, session, model, text, files=None):
        conversation = str(uuid.uuid4())
        body = {
            "endpoint": "AI Gateway", "endpointType": "custom", "model": model,
            "text": text, "conversationId": conversation, "messageId": str(uuid.uuid4()),
            "parentMessageId": "00000000-0000-0000-0000-000000000000",
            "isContinued": False, "isTemporary": False, "max_tokens": 16,
        }
        if files:
            body["files"] = files
        status, data, headers = self.ui_api(session, "/api/agents/chat/AI%20Gateway", body)
        return status, data, headers, conversation

    def checks(self):
        self.launch_librechat()
        sessions = {name: self.login(name) for name in ("alice", "bob", "operator")}
        for name in ("alice", "bob"):
            status, data, _ = self.authorize(sessions[name]["token"])
            check(status == 200 and json.loads(data)["user_id"] == self.policies[name]["user_id"], "LibreChat did not retain the caller's validated access-token identity")
        check(self.authorize(sessions["operator"]["token"])[0] == 401, "UI signup implicitly provisioned Gateway access")
        print("PASS LibreChat real browser OAuth/PKCE and stable Gateway identity", flush=True)

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
            check(status == 200 and "text/event-stream" in headers.get("Content-Type", "") and b"test response" in data, "full LibreChat SSE chat failed")
            status, history, _ = self.ui_api(session, "/api/messages/" + conversation)
            check(status == 200 and b"test response" in history, "LibreChat history was not persisted")
            other = sessions["bob" if name == "alice" else "alice"]
            status, history, _ = self.ui_api(other, "/api/messages/" + conversation)
            check(status in (200, 403, 404) and b"test response" not in history, "LibreChat leaked another user's history")
            before = identity.Provider.calls
            self.chat(session, hidden, "forbidden model fixture " + name)
            check(identity.Provider.calls == before, "saved UI model selection bypassed Gateway grants")
            status, refreshed, _ = self.ui_api(session, "/api/auth/refresh", {})
            check(status == 200 and json.loads(refreshed).get("token"), "LibreChat server refresh failed")
            session["token"] = json.loads(refreshed)["token"]
            status, identity_data, _ = self.authorize(session["token"])
            check(status == 200 and json.loads(identity_data)["credential_id"] == self.identities[name]["credential_id"], "refresh split billing identity")
            model_check(name)
        print("PASS per-user model cache, concurrent catalog, full SSE chat/history, hidden models and refresh", flush=True)

        # No valid shared credential exists. Even a real authenticated UI user
        # without a Gateway binding cannot cause upstream execution.
        before = identity.Provider.calls
        self.chat(sessions["operator"], "model-common", "unprovisioned fixture")
        check(identity.Provider.calls == before, "missing binding fell back to a shared key")
        for name in ("alice", "bob"):
            user = self.policies[name]["user_id"]
            # IDs are fixture-generated; never interpolate user-provided SQL.
            check(re.fullmatch(r"identity-[a-f0-9]+-(alice|bob)", user) is not None, "unsafe fixture identifier")
            result = self.sql(self.billing_dsn, "SELECT count(DISTINCT credential_id),count(*),sum(actual_tokens) FROM billing_budget_reservations WHERE user_id='" + user + "' AND state='committed';").split("|")
            check(result[0] == "1" and int(result[1]) > 0 and int(result[2]) > 0, "LibreChat billing attribution missing or split")
        limited = copy.deepcopy(self.policies["bob"])
        limited["rate_limit_rpm"] = 1
        self.internal("PUT", "/jwt-principals", limited)
        before = identity.Provider.calls
        _, data, _, _ = self.chat(sessions["bob"], "model-b", "quota fixture")
        check(identity.Provider.calls == before and (b"error" in data.lower() or b"429" in data), "Gateway quota failure was hidden or bypassed")
        self.internal("PUT", "/jwt-principals", self.policies["bob"])
        self.internal("PUT", "/users/" + self.policies["alice"]["user_id"], {"status": "disabled", "roles": ["user"]})
        before = identity.Provider.calls
        self.chat(sessions["alice"], "model-a", "disabled user fixture")
        check(identity.Provider.calls == before, "live UI session bypassed deprovisioning")
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
