"""Offline regression contracts for the committed Keycloak profile."""

import importlib.util
import http.cookiejar
import urllib.request
import json
from pathlib import Path
import unittest

ROOT = Path(__file__).resolve().parent.parent


class ProfileTests(unittest.TestCase):
    def test_clients_are_scoped_and_secret_free(self):
        realm = json.loads((ROOT / "examples/identity/keycloak-realm.json").read_text())
        self.assertNotIn("users", realm)
        clients = {c["clientId"]: c for c in realm["clients"]}
        self.assertTrue(clients["gateway"]["bearerOnly"])
        for name in ("openwebui", "gateway-console", "librechat"):
            client = clients[name]
            self.assertNotIn("secret", client)
            self.assertIn("basic", client["defaultClientScopes"])
            self.assertFalse(client["publicClient"])
            self.assertFalse(client["directAccessGrantsEnabled"])
            self.assertFalse(client["serviceAccountsEnabled"])
            self.assertFalse(client["fullScopeAllowed"])
            self.assertEqual(client["attributes"]["pkce.code.challenge.method"], "S256")
            self.assertTrue(
                all(
                    uri.startswith("https://") and "*" not in uri
                    for uri in client["redirectUris"]
                )
            )
            audience = next(
                m
                for m in client["protocolMappers"]
                if m["protocolMapper"] == "oidc-audience-mapper"
            )
            self.assertEqual(audience["config"]["included.client.audience"], "gateway")
            self.assertEqual(audience["config"]["id.token.claim"], "false")
            self.assertEqual(audience["config"]["access.token.claim"], "true")
            roles = next(m for m in client["protocolMappers"] if m["protocolMapper"] == "oidc-usermodel-client-role-mapper")
            self.assertEqual(roles["config"]["id.token.claim"], "true" if name == "gateway-console" else "false")
        self.assertNotEqual(
            clients["openwebui"]["redirectUris"],
            clients["gateway-console"]["redirectUris"],
        )
        self.assertEqual(
            clients["librechat"]["redirectUris"],
            ["https://librechat.example.com/oauth/openid/callback"],
        )
        self.assertEqual(len({tuple(c["redirectUris"]) for c in clients.values() if "redirectUris" in c}), 3)
        scopes = realm["clientScopeMappings"]["gateway"]
        self.assertEqual({entry["client"] for entry in scopes}, {"openwebui", "librechat", "gateway-console"})

    def test_librechat_uses_gateway_authority_and_user_access_token(self):
        # JSON is a YAML 1.2 subset; keep this profile readable without adding a
        # Python dependency to CI's offline configuration contract tests.
        config = json.loads((ROOT / "examples/librechat/librechat.yaml").read_text())
        self.assertFalse(config["balance"]["enabled"])
        self.assertFalse(config["transactions"]["enabled"])
        self.assertEqual(set(config["endpoints"]), {"custom"})
        endpoint, = config["endpoints"]["custom"]
        self.assertEqual(endpoint["baseURL"], "${AI_GATEWAY_BASE_URL}")
        self.assertEqual(endpoint["headers"], {"Authorization": "Bearer {{LIBRECHAT_OPENID_ACCESS_TOKEN}}"})
        self.assertEqual(endpoint["apiKey"], "disabled-no-shared-gateway-key")
        self.assertEqual(endpoint["models"], {"default": [], "fetch": True})
        self.assertEqual(endpoint["titleModel"], "current_model")
        self.assertNotIn("titleEndpoint", endpoint)
        self.assertNotIn("tokenConfig", endpoint)
        self.assertEqual(config["registration"]["socialLogins"], ["openid"])
        self.assertEqual(config["fileConfig"]["endpoints"]["AI Gateway"]["totalSizeLimit"], 8)

    def test_librechat_environment_has_no_provider_or_shared_gateway_key(self):
        text = (ROOT / "examples/librechat/.env.example").read_text()
        env = dict(line.split("=", 1) for line in text.splitlines() if line and not line.startswith("#"))
        for name in ("OPENID_USE_PKCE", "OPENID_GENERATE_NONCE", "OPENID_REUSE_TOKENS", "SESSION_COOKIE_SECURE"):
            self.assertEqual(env[name], "true")
        self.assertEqual(env["OPENID_CLIENT_ID"], "librechat")
        self.assertEqual(env["OPENID_AUDIENCE"], "gateway")
        self.assertEqual(env["ENDPOINTS"], "custom,agents")
        self.assertEqual(env["ALLOW_EMAIL_LOGIN"], "false")
        for name in ("OPENAI_API_KEY", "ANTHROPIC_API_KEY", "AZURE_API_KEY", "AI_GATEWAY_API_KEY"):
            self.assertNotIn(name, env)
        self.assertTrue(env["OPENID_CLIENT_SECRET"].startswith("REQUIRED_"))
        self.assertTrue(env["MONGO_URI"].startswith("REQUIRED_"))
        compose = (ROOT / "examples/librechat/compose.yaml").read_text()
        self.assertIn("@sha256:5b706bcee1ca154a1e2987303aaeca0eb27f97919cccee4bb604226c2f8abf61", compose)
        self.assertNotIn(":latest", compose)
        self.assertIn('user: "1000:1000"', compose)


class HarnessTests(unittest.TestCase):
    def test_secure_cookie_exception_is_loopback_only(self):
        spec = importlib.util.spec_from_file_location(
            "identity_integration", ROOT / "scripts/test-identity-integration.py"
        )
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        cookie = http.cookiejar.Cookie(
            0,
            "fixture",
            "value",
            None,
            False,
            "example.test",
            False,
            False,
            "/",
            True,
            True,
            None,
            True,
            None,
            None,
            {},
        )
        policy = module.LoopbackCookiePolicy()
        for url, expected in (
            ("http://127.0.0.1/oauth", True),
            ("http://example.test/oauth", False),
            ("https://example.test/oauth", True),
            ("http://127.0.0.1.example.test/oauth", False),
        ):
            with self.subTest(url=url):
                self.assertEqual(
                    policy.return_ok_secure(cookie, urllib.request.Request(url)),
                    expected,
                )


if __name__ == "__main__":
    unittest.main()
