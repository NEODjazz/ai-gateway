#!/usr/bin/env bash
# Only use dedicated test databases: integration tests change database contents.
set -euo pipefail
: "${CONTROL_PLANE_POSTGRES_TEST_DSN:?dedicated gateway test database required}"
: "${AUTH_POSTGRES_TEST_DSN:?dedicated auth test database required}"
: "${BILLING_POSTGRES_TEST_DSN:?dedicated billing test database required}"
export POSTGRES_INTEGRATION_REQUIRED=true
project_root=$(cd "$(dirname "$0")/.." && pwd)
for migration in \
  "$project_root"/migrations/postgres/007_gateway_control_plane.sql \
  "$project_root"/migrations/postgres/014_gateway_mcp_tool_calls.sql \
  "$project_root"/migrations/postgres/034_gateway_cached_contents.sql \
  "$project_root"/migrations/postgres/035_gateway_cached_content_policy.sql; do
  psql "$CONTROL_PLANE_POSTGRES_TEST_DSN" -v ON_ERROR_STOP=1 -f "$migration" >/dev/null
done
for migration in "$project_root"/repos/auth/migrations/postgres/*.sql; do
  psql "$AUTH_POSTGRES_TEST_DSN" -v ON_ERROR_STOP=1 -f "$migration" >/dev/null
done
for migration in "$project_root"/repos/billing/migrations/postgres/*.sql; do
  psql "$BILLING_POSTGRES_TEST_DSN" -v ON_ERROR_STOP=1 -f "$migration" >/dev/null
done
for service in gateway auth billing; do
  (cd "$project_root/repos/$service" && go test -race -count=1 ./...)
done
