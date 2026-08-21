#!/usr/bin/env bash
set -euo pipefail

gateway_url="${GATEWAY_URL:-http://127.0.0.1:18080}"
api_key="${API_KEY:-demo-admin-key}"
provider="${OLLAMA_EMBEDDING_PROVIDER:-ollama-embeddings}"
model="${OLLAMA_EMBEDDING_MODEL:-nomic-embed-text:latest}"

command -v curl >/dev/null
command -v jq >/dev/null

payload="$(jq -nc \
  --arg provider "${provider}" \
  --arg model "${model}" \
  '{provider: $provider, model: $model, input: ["embedding smoke one", "embedding smoke two"], encoding_format: "float"}')"

response="$({
  curl --fail-with-body --silent --show-error \
    -H "Authorization: Bearer ${api_key}" \
    -H "Content-Type: application/json" \
    --data "${payload}" \
    "${gateway_url}/v1/embeddings"
})"

jq -e '
  .object == "list" and
  (.data | length) == 2 and
  (.data[0].embedding | length) > 0 and
  (.data[1].embedding | length) > 0 and
  .data[0].index == 0 and
  .data[1].index == 1 and
  .usage.total_tokens > 0
' <<<"${response}" >/dev/null

jq '{model, vectors: (.data | length), dimensions: (.data[0].embedding | length), usage}' <<<"${response}"
