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

## Console management

Open **AI Gateway → Tools → Vector stores**. The page requires the `inference`
console capability and uses the same owned APIs as an API client. Select the
current UI session or enter a virtual key to manage that key's resources. The
additional key is held only in memory, sent only to this Gateway, and excluded
from URLs and browser storage. Changing credentials clears pending requests,
drafts, document previews and search history. Administrative roles do not bypass
storage ownership.

The directory supports literal search, status filters, supported server sorting,
column selection and exact pagination. Create or edit a store's name,
description (stored as metadata), additional metadata and inactivity expiry.
An existing expiry policy can be changed but cannot be cleared through the
current API. Deleting a store requires confirmation; source files remain.

In **Documents**, upload UTF-8 TXT, Markdown, CSV or JSON, or attach an existing
owned `assistants` file. Each file goes through atomic `/v1/rag/ingest` with
attributes and automatic or static chunking. Uploads run sequentially, stop on
the first failure, and retain confirmed successes when retrying. Stop finishes
the current file before pausing. A timeout or lost response is uncertain: refresh
before retrying to avoid creating another source file. The UI allows at most
20 files and 8 MiB per selection, with a 1 MiB per-file ingestion limit. Binary
PDF/DOCX need prior text conversion; deployment document processing for inference
does not change these RAG ingestion constraints.

Document pages use API cursors and do not invent filtered totals. Inspect text
chunks, edit scalar attributes or remove an attachment without deleting its
source file. PostgreSQL attachment responses now include optional `filename`,
resolved in the existing owned joins instead of issuing one metadata request per
row. This is an additive API field; existing response fields and cursors remain.

In **Test vector store**, select an authorized embeddings-capable model, enter
a query and optionally an attribute filter. Model discovery lists authorized IDs;
actual capabilities, authorization, guardrails, quota and billing are checked by
the backend. Results show filenames, similarity scores, attributes and text.
Only the last five successful queries are kept in memory, with an explicit clear
action. Failed requests do not overwrite successful results.

Search currently computes embeddings on demand and supports at most 20 documents,
1 MiB of combined text and 100 chunks per request. It reports a limit error rather
than silently searching a partial collection. A completed store is durable file
storage with chunking settings, not a precomputed external vector index. This UI
does not register external stores or add provider adapters.
