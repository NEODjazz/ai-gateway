#!/usr/bin/env bash
set -euo pipefail

# Run with API_KEY set to a dedicated test credential. Set
# OLLAMA_EMBEDDING_PROVIDER only to target a specific configured endpoint.
gateway_url="${GATEWAY_URL:-http://127.0.0.1:18080}"
: "${API_KEY:?API_KEY must be set to a dedicated test credential}"
api_key="${API_KEY}"
provider="${OLLAMA_EMBEDDING_PROVIDER:-}"
model="${OLLAMA_EMBEDDING_MODEL:-nomic-embed-text:latest}"

command -v curl >/dev/null
command -v jq >/dev/null

payload="$(jq -nc \
  --arg provider "${provider}" \
  --arg model "${model}" \
  '{model: $model, input: ["embedding smoke one", "embedding smoke two"], encoding_format: "float"} +
   (if $provider == "" then {} else {provider: $provider} end)')"

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
