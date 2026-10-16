# Vector storage

Vector stores and their documents belong to the exact authenticated credential
and user. An administrator session does not implicitly gain access to another
credential's stores. Files, document attributes, content and search retain the
same ownership checks as the public storage APIs.

## Directory API

`GET /v1/vector_stores/page` is an additive Gateway extension for management UIs.
It accepts `offset` (0–1000000), `limit` (1–200, default 25), `search` (up to 256
Unicode characters), `status` (`completed` or `expired`), `sort_by` (`name`, `id`,
`created_at` or `last_active_at`) and `sort_order` (`asc` or `desc`). Defaults sort
by descending creation time. ID is a stable tie-breaker. Search is a literal,
case-insensitive substring of the ID, name or `description` metadata; `%` and `_`
are ordinary characters.

The response contains `object: list`, `data`, `total`, `offset` and `limit`.
`total` is the exact filtered owner-scoped count, including an empty late page.
Counts, page rows, expiry status and file summaries share a PostgreSQL
repeatable-read snapshot. Expired attached files do not inflate bytes/counts.
Unknown/repeated query parameters and invalid ranges are rejected. Authentication,
effective access policy and the existing RPM admission path apply to these reads.
Backend failures remain visible errors, never a successful empty directory.

The existing cursor-based `GET /v1/vector_stores` and all creation, update, file,
batch and search APIs remain unchanged. Storage backends can implement the
optional `vectorstate.PageStore` interface; the PostgreSQL store implements it.
Other backends fail this extension explicitly with HTTP 503 rather than silently
scanning an incomplete cursor page. No migration or new dependency is required.
