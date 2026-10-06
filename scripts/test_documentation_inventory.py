import importlib.util
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch


spec = importlib.util.spec_from_file_location(
    "documentation_inventory", Path(__file__).with_name("update-documentation.py"))
inventory = importlib.util.module_from_spec(spec)
spec.loader.exec_module(inventory)


class DocumentationInventoryTests(unittest.TestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.root = Path(directory.name)
        mock = patch.object(inventory, "ROOT", self.root)
        mock.start()
        self.addCleanup(mock.stop)

    def write(self, path, contents):
        target = self.root / path
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text(contents)

    def api_fixture(self):
        self.write("repos/gateway/api/openapi.yaml", """openapi: 3.1.0
paths:
  /v1/models:
    get:
      operationId: listModels
      tags: [Models]
  /v1beta/models/{model}:countTokens:
    post:
      operationId: countTokens
      tags: [Inference]
components:
  schemas: {}
""")
        self.write("repos/gateway/internal/gateway/routes.go",
                   '{RouteContract{http.MethodGet, "/v1/models"}, handler}')
        self.write("repos/gateway/internal/gateway/generate.go",
                   '{http.MethodPost, "/v1beta/models/{model}:countTokens"}')

    def test_includes_native_action_path_and_distinct_methods(self):
        self.api_fixture()
        rendered = inventory.api_index()
        self.assertIn('`POST` | `/v1beta/models/{model}:countTokens`', rendered)
        self.assertIn('`GET` | `/v1/models`', rendered)
        self.assertIn('2 HTTP-операции (2 путей)', rendered)

    def test_route_mismatch_fails_closed(self):
        self.api_fixture()
        self.write("repos/gateway/internal/gateway/generate.go", "")
        with self.assertRaisesRegex(ValueError, "Route/OpenAPI mismatch"):
            inventory.api_index()

    def test_missing_operation_id_fails(self):
        self.api_fixture()
        source = self.root / "repos/gateway/api/openapi.yaml"
        source.write_text(source.read_text().replace("      operationId: countTokens\n", ""))
        with self.assertRaisesRegex(ValueError, "Missing operationId"):
            inventory.api_index()

    def test_environment_inventory_excludes_test_reads(self):
        self.write("repos/auth/main.go", 'os.Getenv("AUTH_REAL")\nenvInt("AUTH_LIMIT", 8)')
        self.write("repos/auth/main_test.go", 'os.Getenv("AUTH_TEST_ONLY")')
        self.write("repos/auth/vendor/library/main.go", 'os.Getenv("VENDOR_ONLY")')
        self.write("repos/auth/node_modules/library/main.go", 'os.Getenv("NODE_ONLY")')
        rendered = inventory.environment_reference()
        self.assertIn('`AUTH_REAL`', rendered)
        self.assertIn('`AUTH_LIMIT`', rendered)
        self.assertNotIn('AUTH_TEST_ONLY', rendered)
        self.assertNotIn('VENDOR_ONLY', rendered)
        self.assertNotIn('NODE_ONLY', rendered)

    def test_ui_capability_drift_and_duplicate_navigation_fail(self):
        self.write("repos/gateway/ui/src/app/routes.tsx", """export const appRoutes: AppRoute[] = [
  { path: "/usage", capability: "organization_reports" },
  { path: "/usage/:id", navigation: false }
];""")
        self.write("repos/gateway/ui/src/app/navigation.ts", """export const navigationSections: NavigationSection[] = [
  { items: ["/usage"] }
];""")
        self.write("docs/admin-ui.md", "| `/usage` | `organization_reports` |")
        inventory.check_ui_routes()
        self.write("docs/admin-ui.md", "| `/usage` | `admin` |")
        with self.assertRaisesRegex(ValueError, "UI routes/capabilities differ"):
            inventory.check_ui_routes()
        self.write("docs/admin-ui.md", "| `/usage` | `organization_reports` |")
        self.write("repos/gateway/ui/src/app/navigation.ts", """export const navigationSections: NavigationSection[] = [
  { items: ["/usage", "/usage"] }
];""")
        with self.assertRaisesRegex(ValueError, "UI routes/capabilities differ"):
            inventory.check_ui_routes()


if __name__ == "__main__":
    unittest.main()
