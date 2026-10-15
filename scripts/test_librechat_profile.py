"""Offline acceptance-harness contracts; no Docker, network, or real credentials."""
import importlib.util
import io
import json
from pathlib import Path
from types import SimpleNamespace
import unittest
from unittest.mock import Mock, patch

spec = importlib.util.spec_from_file_location("librechat_integration", Path(__file__).with_name("test-librechat-integration.py"))
integration = importlib.util.module_from_spec(spec)
spec.loader.exec_module(integration)


class HarnessTests(unittest.TestCase):
    def observe_provider_request(self, body):
        handler = object.__new__(integration.ObservedProvider)
        handler.rfile = io.BytesIO(body)
        handler.headers = {"Content-Length": str(len(body))}
        handler.path = "/v1/chat/completions"
        # Only exercise recording; response generation is independently tested
        # through the full real-service scenario.
        with patch.object(integration.ObservedProvider.__bases__[0], "do_POST"):
            handler.do_POST()

    def test_title_request_cannot_erase_document_delivery_evidence(self):
        marker = "LC_DOC_REQUEST_" + "a" * 32
        document = "LC_DOCUMENT_" + "b" * 32
        with patch.object(integration.ObservedProvider, "document_deliveries", {}):
            self.observe_provider_request((marker + " " + document).encode())
            self.observe_provider_request(marker.encode())
            self.assertEqual(integration.ObservedProvider.document_deliveries[marker], {document})

    def test_privacy_evidence_distinguishes_indexed_mask_from_raw_email(self):
        marker = "LC_PRIVACY_" + "c" * 32
        with patch.object(integration.ObservedProvider, "privacy_deliveries", {}):
            self.observe_provider_request((marker + " {{EMAIL_1}}").encode())
            self.assertEqual(integration.ObservedProvider.privacy_deliveries[marker], {"masked_email"})
            self.observe_provider_request((marker + " qa@example.test").encode())
            self.assertEqual(integration.ObservedProvider.privacy_deliveries[marker], {"masked_email", "raw_email"})

    def test_ui_requests_use_the_browser_route_contract(self):
        runner = object.__new__(integration.Run)
        runner.ui = "http://127.0.0.1:3080"
        session = {"token": "fixture-token", "opener": object()}
        with patch.object(integration, "request_http", return_value=(200, b"{}", {})) as request:
            runner.ui_api(session, "/api/models")
        self.assertEqual(request.call_args.args[2]["User-Agent"], integration.BROWSER_USER_AGENT)
        self.assertEqual(request.call_args.args[2]["Authorization"], "Bearer fixture-token")

    def test_partial_or_failed_stream_is_not_success(self):
        for event in (
            {"delta": "test response"},
            {"final": True},
            {"final": True, "reconcile": True, "responseMessage": {"text": "test response"}},
            {"final": True, "responseMessage": {"text": "test response", "unfinished": True}},
            {"final": True, "error": "provider failed", "responseMessage": {"text": "test response"}},
        ):
            with self.subTest(event=event):
                self.assertFalse(integration.completed_chat(("data: " + json.dumps(event) + "\n\n").encode()))
        terminal = {"final": True, "responseMessage": {"content": [{"type": "text", "text": "test response"}]}}
        self.assertTrue(integration.completed_chat(("event: message\ndata: " + json.dumps(terminal) + "\n\n").encode()))

    def runner(self, responses):
        # Avoid constructing databases, listeners, or containers in unit tests.
        runner = object.__new__(integration.Run)
        runner.ui_api = Mock(side_effect=responses)
        return runner

    def test_chat_subscribes_to_the_exact_started_generation(self):
        runner = self.runner([
            (200, json.dumps({"status": "started", "streamId": "fixture-stream", "conversationId": "fixture-conversation", "generationCreatedAt": 123}).encode(), {"Content-Type": "application/json"}),
            (200, b'data: {"final": true}\n\n', {"Content-Type": "text/event-stream"}),
        ])
        status, data, headers, _ = runner.chat({}, "model-a", "fixture")
        self.assertEqual(status, 200)
        self.assertIn(b'"final": true', data)
        self.assertEqual(headers["Content-Type"], "text/event-stream")
        self.assertEqual(runner.ui_api.call_args_list[0].args[2]["conversationId"], "new")
        self.assertEqual(runner.ui_api.call_args_list[1].args[1], "/api/agents/chat/stream/fixture-stream?generationCreatedAt=123")

    def test_failed_start_does_not_subscribe(self):
        runner = self.runner([(403, b'{"error":"forbidden"}', {"Content-Type": "application/json"})])
        self.assertEqual(runner.chat({}, "hidden-model", "fixture")[0], 403)
        self.assertEqual(runner.ui_api.call_count, 1)

    def test_malformed_generation_identity_is_rejected(self):
        for generation in (None, True, -1, "123", 9007199254740992):
            with self.subTest(generation=generation):
                runner = self.runner([(200, json.dumps({"status": "started", "streamId": "fixture", "conversationId": "fixture", "generationCreatedAt": generation}).encode(), {"Content-Type": "application/json"})])
                with self.assertRaisesRegex(RuntimeError, "generation identity"):
                    runner.chat({}, "model-a", "fixture")
                self.assertEqual(runner.ui_api.call_count, 1)

    def test_negative_case_requires_an_actual_failure_response(self):
        runner = self.runner([])
        runner.chat = Mock(return_value=(200, b'data: {"final":true}', {}, "fixture"))
        with self.assertRaisesRegex(RuntimeError, "UI did not report failure"):
            runner.assert_rejected({}, "model-a", "expected denial")

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
