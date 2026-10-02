#!/usr/bin/env python3
"""Check shared encryption Secret references without printing rendered secrets."""
import base64
from pathlib import Path
import re
import secrets
import subprocess
import unittest

ROOT = Path(__file__).resolve().parent.parent


def render(chart, *settings, ok=True):
    args = ["helm", "template", chart, str(ROOT / "charts" / chart)]
    for setting in settings:
        args += ["--set-string", setting]
    result = subprocess.run(args, capture_output=True, text=True)
    if (result.returncode == 0) != ok:
        raise AssertionError("unexpected Helm rendering result")
    return result.stdout


def env_reference(rendered, name):
    match = re.search(r"- name: " + re.escape(name) + r"\n(.*?)(?=\n\s*- name:|\n\s*ports:)", rendered, re.S)
    if not match:
        raise AssertionError("expected encryption environment reference missing")
    return match.group(1)


class CredentialEncryptionCharts(unittest.TestCase):
    def test_shared_external_secret(self):
        for chart in ("ai-gateway", "auth"):
            with self.subTest(chart=chart):
                rendered = render(chart, "credentialEncryption.existingSecret=shared-encryption", "credentialEncryption.secretKey=selected-key")
                reference = env_reference(rendered, "CREDENTIAL_ENCRYPTION_KEY")
                self.assertIn('name: "shared-encryption"', reference)
                self.assertIn('key: "selected-key"', reference)
                self.assertNotIn("optional: true", reference)
                self.assertNotIn("- name: PROVIDER_CREDENTIAL_ENCRYPTION_KEY", rendered)
                self.assertNotIn("  CREDENTIAL_ENCRYPTION_KEY:", rendered)

    def test_inline_and_legacy_compatibility(self):
        key = secrets.token_hex(16)
        for chart in ("ai-gateway", "auth"):
            with self.subTest(chart=chart):
                rendered = render(chart, "credentialEncryption.key=" + key)
                self.assertIn("key: CREDENTIAL_ENCRYPTION_KEY", env_reference(rendered, "CREDENTIAL_ENCRYPTION_KEY"))
                stored = key if chart == "ai-gateway" else base64.b64encode(key.encode()).decode()
                self.assertTrue('CREDENTIAL_ENCRYPTION_KEY: "' + stored + '"' in rendered, "generated Secret does not contain the shared key")
                render(chart, "credentialEncryption.key=" + key, "credentialEncryption.existingSecret=shared", ok=False)
                render(chart, "credentialEncryption.existingSecret=shared", "credentialEncryption.secretKey=", ok=False)
        rendered = render("ai-gateway", "gateway.controlPlane.credentialEncryptionKey=" + key)
        self.assertTrue('CREDENTIAL_ENCRYPTION_KEY: "' + key + '"' in rendered, "legacy Helm values lost existing key")
        render("ai-gateway", "credentialEncryption.key=" + key, "gateway.controlPlane.credentialEncryptionKey=different", ok=False)
        render("ai-gateway", "gateway.controlPlane.credentialEncryptionKey=" + key, "credentialEncryption.existingSecret=shared", ok=False)
        render("ai-gateway", "credentialEncryption.key=" + key, "gateway.controlPlane.credentialEncryptionKey=" + key)

    def test_default_compatibility(self):
        auth = render("auth")
        self.assertNotIn("- name: CREDENTIAL_ENCRYPTION_KEY", auth)
        self.assertIn("key: key-hash-secret", env_reference(auth, "AUTH_KEY_HASH_SECRET"))
        gateway = render("ai-gateway")
        self.assertIn("optional: true", env_reference(gateway, "CREDENTIAL_ENCRYPTION_KEY"))
        self.assertIn("key: PROVIDER_CREDENTIAL_ENCRYPTION_KEY", env_reference(gateway, "PROVIDER_CREDENTIAL_ENCRYPTION_KEY"))


if __name__ == "__main__":
    unittest.main()
