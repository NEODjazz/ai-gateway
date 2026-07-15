# AI Gateway

A fast, modular AI gateway written in Go with OpenAI API compatibility.

## Overview

The gateway accepts requests in the OpenAI API format, runs gateway-level functions, and forwards each request to the provider router. Provider-level functions run as part of a specific attempt to call an AI provider endpoint. Each function can be required or optional:

- `required: true` — the request stops if the function is unavailable or returns an error.
- `required: false` — the request continues without the function if it is unavailable.

The current processing levels are:

- Gateway level: `auth`.
- Provider level: `anonymizer`, `dlp`, `av`, and `billing`.

## Microservices

- `gateway` — the central request entry point, OpenAI-compatible API, routing, and function pipeline.
- `anonymizer` — replaces sensitive information and caches mappings in Redis.
- `auth` — authorization, API keys, JWTs, roles, and permissions.
- `billing` — tracks usage, tokens, and costs by user, key, model, and provider.
- `dlp` — scans content for data leaks through ICAP.
- `av` — scans content for malware through ICAP.

## Quick start

```powershell
cd repos/gateway
go run ./cmd/gateway
```

Test the gateway:

```powershell
Invoke-RestMethod -Method Post http://localhost:8080/v1/chat/completions `
  -Headers @{ Authorization = "Bearer demo-admin-key"; "Content-Type" = "application/json" } `
  -Body '{"model":"demo-model","messages":[{"role":"user","content":"hello from user@example.com"}]}'
```

## Run in Rancher Desktop with Helm

Each service is installed as a separate Helm chart:

- `charts/ai-gateway` — gateway only.
- `charts/auth` — authorization.
- `charts/anonymizer` — anonymization.
- `charts/billing` — billing.
- `charts/security` — DLP and antivirus.
- `charts/redis`, `charts/postgres`, and `charts/clickhouse` — data stores.

```powershell
kubectl create namespace ai-gateway --dry-run=client -o yaml | kubectl apply -f -
helm upgrade --install ai-gateway-redis charts/redis --namespace ai-gateway
helm upgrade --install ai-gateway-postgres charts/postgres --namespace ai-gateway
helm upgrade --install ai-gateway-clickhouse charts/clickhouse --namespace ai-gateway
helm upgrade --install ai-gateway-auth charts/auth --namespace ai-gateway
helm upgrade --install ai-gateway-anonymizer charts/anonymizer --namespace ai-gateway
helm upgrade --install ai-gateway-billing charts/billing --namespace ai-gateway
helm upgrade --install ai-gateway-security charts/security --namespace ai-gateway
helm upgrade --install ai-gateway charts/ai-gateway --namespace ai-gateway
kubectl -n ai-gateway get pods
```

The release names in this example match the module URLs in `charts/ai-gateway/values.yaml`. If you use different release names, set `gateway.modules.<module>.url` explicitly.

When migrating from the previous combined release, upgrade `ai-gateway` first so Helm can remove the old module Deployments and Services, then install the new service releases. Otherwise, Helm cannot adopt existing resources owned by another release.

`devMode` is enabled by default. Services run directly from the current project directory using the `golang:1.22-alpine` image, so no local Docker image build is required. This is convenient for quickly testing the microservices in Rancher Desktop.

### Ollama provider

In Rancher Desktop, the chart connects to a local Ollama instance on the Windows host by default and includes a demo fallback:

```text
DEFAULT_PROVIDER=ollama
PROVIDERS_JSON=[...]
```

Set the model with the `model` field. You can select a provider with the extended `provider` field. If `provider` is omitted, the gateway uses `DEFAULT_PROVIDER`. If `provider` is set to `"auto"`, the gateway selects the first suitable endpoint based on model compatibility and priority.

```powershell
Invoke-RestMethod -Method Get http://127.0.0.1:11434/api/tags
```

Example request through the gateway:

```powershell
Invoke-RestMethod -Method Post http://127.0.0.1:18080/v1/chat/completions `
  -Headers @{ Authorization = "Bearer demo-admin-key"; "Content-Type" = "application/json" } `
  -Body '{"provider":"ollama","model":"lfm2.5-thinking:1.2b","messages":[{"role":"user","content":"Reply with exactly this text and nothing else: user@example.com"}]}'
```

The Responses API is also supported:

```powershell
Invoke-RestMethod -Method Post http://127.0.0.1:18080/v1/responses `
  -Headers @{ Authorization = "Bearer demo-admin-key"; "Content-Type" = "application/json" } `
  -Body '{"provider":"ollama","model":"lfm2.5-thinking:1.2b","input":"Reply with exactly this text and nothing else: user@example.com"}'
```

For `/v1/responses`, the gateway uses the same flow: gateway-level authentication, provider routing and failover, provider-level anonymization, DLP, antivirus, and billing, followed by response deanonymization. Basic text support is currently implemented for `input`, `instructions`, `max_output_tokens`, `output_text`, and `output[].content[].text`. Streaming and tools will be added as a separate layer.

### Multiple providers and failover

Provider endpoints are configured through the `gateway.providers` list in the Helm values. A provider type can have multiple connections with different keys and priorities:

```yaml
gateway:
  provider:
    default: openrouter
  providers:
    - name: openrouter-key-a
      type: openai-compatible
      base_url: https://openrouter.ai/api
      api_key: key-a
      dlp_enabled: true
      av_enabled: true
      models:
        - openai/gpt-4o-mini
      priority: 10
      enabled: true
    - name: openrouter-key-b
      type: openai-compatible
      base_url: https://openrouter.ai/api
      api_key: key-b
      models:
        - openai/gpt-4o-mini
      priority: 20
      enabled: true
    - name: ollama-local
      type: ollama
      base_url: http://host.docker.internal:11434
      priority: 100
      enabled: true
```

The gateway tries endpoints in `priority` order. If an endpoint returns an error or is unavailable, the router automatically tries the next compatible endpoint.

Request with an explicit provider:

```json
{
  "provider": "openrouter",
  "model": "openai/gpt-4o-mini",
  "messages": [
    {"role": "user", "content": "hello"}
  ]
}
```

The `provider` field accepts either a provider `type` (`ollama`, `openai-compatible`, or `openrouter`) or a specific endpoint name such as `openrouter-key-a`.

### DLP and antivirus through ICAP

The `dlp` and `av` modules run at the provider level before anonymization and before the request is sent to an AI provider endpoint. The gateway invokes them as remote HTTP modules, while the separate `repos/dlp` and `repos/av` microservices perform the ICAP calls.

Enable these modules separately for each endpoint in `PROVIDERS_JSON` or Helm `gateway.providers`:

```yaml
gateway:
  modules:
    dlp:
      required: true
      remote: true
    av:
      required: true
      remote: true
  providers:
    - name: openrouter-key-a
      type: openai-compatible
      dlp_enabled: true
      av_enabled: true
dlp:
  enabled: true
  icap:
    host: dlp.example.local
    port: "1344"
    service: /reqmod
av:
  enabled: true
  icap:
    host: av.example.local
    port: "1344"
    service: /avscan
```

When running without Helm, use these variables:

```text
DLP_URL=http://localhost:8084
AV_URL=http://localhost:8085
DLP_REQUIRED=true
AV_REQUIRED=true
```

Configure the ICAP connections for the `dlp` and `av` microservices with these environment variables:

```text
DLP_ICAP_HOST=dlp.example.local
DLP_ICAP_PORT=1344
DLP_ICAP_SERVICE=/reqmod
DLP_ICAP_TIMEOUT=5s
AV_ICAP_HOST=av.example.local
AV_ICAP_PORT=1344
AV_ICAP_SERVICE=/avscan
AV_ICAP_TIMEOUT=5s
```

If an endpoint does not define `dlp_enabled` or `av_enabled`, the gateway does not call the corresponding microservice. The microservices send the request text using ICAP `REQMOD`. An ICAP `204` response or a regular successful `2xx` response allows the request to proceed. Block or infection indicators in the ICAP headers are returned to the gateway as a `451` response and stop the current provider attempt.

### Authentication: API keys and JWTs

The gateway reads the token from the standard header:

```text
Authorization: Bearer <token>
```

The authentication service first checks the demo API keys:

- `demo-admin-key`
- `demo-user-key`

If the key is not found, the service tries to validate it as a JWT. HS256 is currently supported:

```text
AUTH_JWT_SECRET=dev-jwt-secret
AUTH_JWT_ISSUER=ai-gateway
AUTH_JWT_AUDIENCE=ai-gateway
```

Minimum claims:

```json
{
  "sub": "user-123",
  "roles": ["developer"],
  "iss": "ai-gateway",
  "aud": "ai-gateway",
  "exp": 1790000000
}
```

`sub` becomes `UserID`, while `roles` or `role` is passed into the role model. In production, store the secret as a Kubernetes Secret rather than in a regular `values.yaml` file.

### Anonymization configuration

Use the `ANONYMIZER_RULES` variable or the `anonymizer.rules` Helm value to choose the enabled rules. Rule definitions live in `anonymizer.ruleDefinitions` in the Helm chart. Each rule can define a `name`, `placeholder`, regular-expression `pattern`, and optional `validator`. The `luhn` validator is currently used for bank card numbers. When the definitions change, Helm updates the ConfigMap and automatically restarts the anonymizer deployment.

Anonymization is reversible within a single provider attempt. Before sending a request to a specific AI provider endpoint, the router replaces sensitive values with unique placeholders such as `{{EMAIL_1}}`. After the model responds, it restores the original values in the text returned to the user.

Available rules:

- `email` — email addresses.
- `phone` — phone numbers.
- `person_ru` — full names in Russian.
- `address_ru` — addresses containing Russian abbreviations such as `г.`, `ул.`, `д.`, and `кв.`.
- `passport_ru` — Russian passport numbers in the `4510 123456` format.
- `inn` — Russian taxpayer identification numbers containing 10 or 12 digits.
- `bank_card` — bank card numbers validated with the Luhn algorithm.
- `ip` — IPv4 addresses.
- `api_key` — API keys, access tokens, bearer tokens, and client secrets.
- `jwt` — JWTs.
- `secret` — passwords and similar secrets.

Examples:

```powershell
# Enable all rules
helm upgrade --install ai-gateway-anonymizer charts/anonymizer --namespace ai-gateway --set anonymizer.rules=all

# Enable only email, phone, and IP rules
helm upgrade --install ai-gateway-anonymizer charts/anonymizer --namespace ai-gateway --set-string anonymizer.rules="email\,phone\,ip"

# Disable all rules
helm upgrade --install ai-gateway-anonymizer charts/anonymizer --namespace ai-gateway --set anonymizer.rules=none
```

Test the gateway:

```powershell
kubectl -n ai-gateway port-forward svc/ai-gateway-gateway 18080:8080
```

In another terminal:

```powershell
Invoke-RestMethod -Method Post http://127.0.0.1:18080/v1/chat/completions `
  -Headers @{ Authorization = "Bearer demo-admin-key"; "Content-Type" = "application/json" } `
  -Body '{"model":"demo-model","messages":[{"role":"user","content":"hello from user@example.com"}]}'
```

For a production-style deployment using custom images:

```powershell
docker build -t ai-gateway:dev .
helm upgrade --install ai-gateway charts/ai-gateway --namespace ai-gateway --set devMode.enabled=false
helm upgrade --install ai-gateway-auth charts/auth --namespace ai-gateway --set devMode.enabled=false
helm upgrade --install ai-gateway-anonymizer charts/anonymizer --namespace ai-gateway --set devMode.enabled=false
helm upgrade --install ai-gateway-billing charts/billing --namespace ai-gateway --set devMode.enabled=false
helm upgrade --install ai-gateway-security charts/security --namespace ai-gateway --set devMode.enabled=false
```

## Structure

```text
cmd/
  gateway/
  anonymizer/
  auth/
  billing/
internal/
  config/
  gateway/
  modules/
  openai/
```

## Split repositories

For easier ongoing development, the monorepo is split into independent Go projects:

```text
repos/
  gateway/
  auth/
  billing/
  dlp/
  av/
  anonymizer/
```

Each directory contains its own `go.mod`, `Dockerfile`, and `README.md`. To test a project:

```powershell
cd repos/gateway
go test ./...
```

The shared contract (`RequestContext` and OpenAI-compatible DTOs) is currently duplicated across the services. The next step is to move it into a separate versioned module or repository, such as `ai-gateway-contracts`, so the DTOs do not need to be synchronized manually.

## GitHub Actions and releases

Pull requests and pushes to `main` or `master` run Go formatting, vet, race-enabled tests, container builds, and Helm lint/render checks. A semantic version tag starts a release:

```powershell
git tag v0.2.0
git push origin v0.2.0
```

The release workflow publishes multi-platform service images to GitHub Container Registry using names such as `ghcr.io/<owner>/ai-gateway-gateway:0.2.0`. It also publishes every chart to the OCI namespace `oci://ghcr.io/<owner>/charts` and attaches the packaged charts to the GitHub Release.

Install a published chart with its matching service image:

```powershell
helm upgrade --install ai-gateway oci://ghcr.io/<owner>/charts/ai-gateway `
  --version 0.2.0 `
  --namespace ai-gateway `
  --create-namespace `
  --set devMode.enabled=false `
  --set image.repository=ghcr.io/<owner>/ai-gateway-gateway `
  --set image.tag=0.2.0
```

The workflows use the built-in `GITHUB_TOKEN`; no additional registry secret is required. In the repository settings, keep Actions workflow permissions enabled and allow the workflow to write packages and repository contents.

New GHCR packages are private by default. For a public repository, change the visibility of the published service images and Helm chart packages to public after the first release.

## Roadmap

1. Connect `anonymizer` to a real Redis instance.
2. Add an adapter/provider layer for OpenAI, Anthropic, Google, local LLMs, and other providers.
3. Store billing events in PostgreSQL or ClickHouse.
4. Replace the in-process auth, anonymizer, and billing modules with HTTP/gRPC clients.
5. Add streaming support for `/v1/chat/completions`.

## License

This project is licensed under the [GNU Affero General Public License v3.0](LICENSE).
