"""Offline acceptance-harness contracts; no Docker, network, or real credentials."""
import importlib.util
from pathlib import Path
from types import SimpleNamespace
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("librechat_integration", Path(__file__).with_name("test-librechat-integration.py"))
integration = importlib.util.module_from_spec(spec)
spec.loader.exec_module(integration)


class HarnessTests(unittest.TestCase):
    def test_missing_image_fails_before_implicit_download(self):
        with patch.object(integration.subprocess, "run", return_value=SimpleNamespace(returncode=1)) as command:
            with self.assertRaisesRegex(RuntimeError, "image is missing"):
                integration.existing_images(["docker", "--context", "rancher-desktop"])
        self.assertEqual(command.call_count, 1)
        self.assertEqual(command.call_args.args[0][:5], ["docker", "--context", "rancher-desktop", "image", "inspect"])

    def test_all_pinned_images_are_checked(self):
        with patch.object(integration.subprocess, "run", return_value=SimpleNamespace(returncode=0)) as command:
            integration.existing_images(["docker"])
        self.assertEqual(command.call_count, 3)
        for call in command.call_args_list:
            self.assertEqual(call.args[0][:3], ["docker", "image", "inspect"])
            self.assertRegex(call.args[0][3], r"@sha256:[0-9a-f]{64}$")

    def test_tls_fixture_does_not_disable_certificate_checks(self):
        source = Path(integration.__file__).read_text()
        self.assertNotIn("NODE_TLS_REJECT_UNAUTHORIZED", source)
        self.assertNotIn("verify=False", source)
        self.assertNotIn("_create_unverified_context", source)
        self.assertIn('NODE_EXTRA_CA_CERTS="/fixture/ca.crt"', source)

    def test_ci_preserves_both_clients(self):
        workflow = (integration.ROOT / ".github/workflows/test.yml").read_text()
        self.assertIn("chat_ui: [librechat, openwebui]", workflow)
        self.assertIn("run: python3 -B scripts/test-identity-integration.py", workflow)
        self.assertIn("run: python3 -B scripts/test-librechat-integration.py", workflow)
        for image in (integration.LIBRECHAT_IMAGE, integration.MONGO_IMAGE):
            self.assertIn("docker pull " + image, workflow)


if __name__ == "__main__":
    unittest.main()
