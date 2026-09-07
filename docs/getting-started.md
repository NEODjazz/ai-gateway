# Быстрый запуск

## Что запускается

Gateway — единственная клиентская точка входа. Auth, Billing, Anonymizer, DLP и
AV являются внутренними сервисами. Для полного managed-режима также нужны
PostgreSQL, ClickHouse и Redis. `docker-compose.yml` запускает только эти три
хранилища и не запускает приложения.

## Минимальный локальный demo

Требуется версия Go из `repos/gateway/go.mod`. Из корня проекта:

```bash
cd repos/gateway
go run ./cmd/gateway
```

Без `PROVIDERS_JSON` gateway создаёт demo provider. Без `AUTH_URL` используется
локальный auth module; built-in demo keys включены по умолчанию. Проверка:

```bash
curl -sS http://127.0.0.1:8080/v1/chat/completions \
  -H 'Authorization: Bearer demo-admin-key' \
  -H 'Content-Type: application/json' \
  -d '{"model":"demo-model","messages":[{"role":"user","content":"hello"}]}'
```

Это режим разработки: данные control plane, rate limits, cache и affinity могут
оставаться process-local. Он не проверяет межсервисные контракты и migrations.

## Хранилища через Docker Compose

При работающем Rancher Desktop:

```bash
docker compose up -d
docker compose ps
```

Compose публикует Redis `6379`, ClickHouse HTTP/native `8123/9000` и PostgreSQL
`5432` с локальными development credentials из файла. Приложения запускаются
отдельно или через Helm. Не используйте эти значения в production.

## Полный стек в Rancher Desktop Kubernetes

Сначала убедитесь, что Kubernetes доступен:

```bash
kubectl cluster-info
kubectl create namespace ai-gateway --dry-run=client -o yaml | kubectl apply -f -
```

Release names ниже соответствуют внутренним URL из default Helm values:

```bash
helm upgrade --install ai-gateway-redis charts/redis -n ai-gateway
helm upgrade --install ai-gateway-postgres charts/postgres -n ai-gateway
helm upgrade --install ai-gateway-clickhouse charts/clickhouse -n ai-gateway
helm upgrade --install ai-gateway-auth charts/auth -n ai-gateway
helm upgrade --install ai-gateway-anonymizer charts/anonymizer -n ai-gateway
helm upgrade --install ai-gateway-billing charts/billing -n ai-gateway
helm upgrade --install ai-gateway-security charts/security -n ai-gateway
helm upgrade --install ai-gateway charts/ai-gateway -n ai-gateway
kubectl get pods -n ai-gateway
```

Default values содержат development secrets и примеры provider endpoints.
Перед установкой подготовьте отдельный values-файл: замените shared secrets,
PostgreSQL/ClickHouse credentials, `AUTH_KEY_HASH_SECRET`, provider credentials
и `PROVIDER_CREDENTIAL_ENCRYPTION_KEY`; выключите demo/static auth fallback.

Для постоянного control plane настройте
`gateway.controlPlane.postgresDsn` и стабильный encryption key длиной не менее
16 символов. Gateway завершит startup при недоступном PostgreSQL, неверном
snapshot или невозможности расшифровать сохранённый secret.

## Доступ без port-forward

Rancher Desktop обычно использует Traefik. В values-файле:

```yaml
gateway:
  ingress:
    enabled: true
    className: traefik
    hosts:
      - host: ai-gateway.localhost
        paths:
          - path: /
            pathType: Prefix
```

После `helm upgrade` проверьте:

```bash
kubectl get ingress -n ai-gateway
curl -i http://ai-gateway.localhost/healthz
```

UI доступен на `/ui/`. Swagger доступен на `/docs/` только при
`gateway.apiDocs.enabled=true`; интерактивные запросы из Swagger дополнительно
требуют `tryItOutEnabled=true`.

## Проверка inference

Получите Virtual Key в UI: plaintext показывается только после create/rotate.
Для проверки модели:

```bash
curl -N http://ai-gateway.localhost/v1/chat/completions \
  -H 'Authorization: Bearer <virtual-key>' \
  -H 'Content-Type: application/json' \
  -d '{"model":"<public-model>","stream":true,"messages":[{"role":"user","content":"Reply OK"}]}'
```

`curl -N` отключает клиентскую буферизацию. Если SSE приходит одним крупным
content chunk, сравните прямой ответ upstream: gateway flush-ит каждое принятое
событие, но provider или его content filter может формировать крупные chunks.
