#!/usr/bin/env python3
"""Apply the rendered PostgreSQL chart migrations to a dedicated test DB."""

import os
import re
import subprocess
import sys


MIGRATION = re.compile(r"^  ([a-z0-9_-]+\.sql): \|$")


def parse_migrations(rendered: str) -> dict[str, str]:
    lines = rendered.splitlines()
    if "kind: ConfigMap" not in lines or "data:" not in lines:
        raise ValueError("rendered PostgreSQL migrations ConfigMap is missing")
    migrations: dict[str, str] = {}
    name = None
    body: list[str] = []
    for line in lines[lines.index("data:") + 1 :]:
        match = MIGRATION.fullmatch(line)
        if match:
            if name is not None:
                migrations[name] = "\n".join(body) + "\n"
            name, body = match.group(1), []
        elif name is not None and line.startswith("    "):
            body.append(line[4:])
        elif name is not None and not line.strip():
            body.append("")
        else:
            raise ValueError("unexpected rendered PostgreSQL migration format")
    if name is not None:
        migrations[name] = "\n".join(body) + "\n"
    required = {
        "001_financial_core.sql",
        "007_gateway_control_plane.sql",
        "013_jwt_principals.sql",
        "014_sso_settings.sql",
        "015_sso_sessions.sql",
        "036_gateway_vector_store_chunking.sql",
        "037_gateway_conversations.sql",
        "038_gateway_conversation_background.sql",
        "039_gateway_response_sessions.sql",
    }
    if not required.issubset(migrations) or any(not sql.strip() for sql in migrations.values()):
        raise ValueError("rendered PostgreSQL migrations are incomplete")
    return migrations


def main() -> int:
    dsn = os.environ.get("CONTROL_PLANE_POSTGRES_TEST_DSN")
    if not dsn:
        print("dedicated gateway test database required", file=sys.stderr)
        return 1
    try:
        migrations = parse_migrations(sys.stdin.read())
    except ValueError as error:
        print(error, file=sys.stderr)
        return 1
    for name, sql in sorted(migrations.items()):
        result = subprocess.run(
            ["psql", "-X", "--dbname", dsn, "-v", "ON_ERROR_STOP=1", "-q"],
            input=sql.encode(),
            stdout=subprocess.DEVNULL,
            stderr=subprocess.PIPE,
            check=False,
        )
        if result.returncode:
            print(f"PostgreSQL chart migration failed: {name}", file=sys.stderr)
            print(result.stderr.decode(errors="replace")[-500:], file=sys.stderr)
            return 1
    print(f"applied {len(migrations)} PostgreSQL chart migrations to test database")
    return 0


if __name__ == "__main__":
    sys.exit(main())
