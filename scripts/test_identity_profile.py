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
        for name in ("openwebui", "gateway-console"):
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
        self.assertNotEqual(
            clients["openwebui"]["redirectUris"],
            clients["gateway-console"]["redirectUris"],
        )


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
