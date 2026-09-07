# Разработка и releases

## Модули

Проект хранится в одном repository, но каждый сервис — отдельный Go module:

| Каталог | Module |
| --- | --- |
| `repos/gateway` | `ai-gateway-gateway` |
| `repos/auth` | `ai-gateway-auth` |
| `repos/billing` | `ai-gateway-billing` |
| `repos/anonymizer` | `ai-gateway-anonymizer` |
| `repos/dlp` | `ai-gateway-dlp` |
| `repos/av` | `ai-gateway-av` |

Используйте Go version из каждого `go.mod`. Container/dev toolchain закреплён в
Dockerfiles и Helm values отдельно; изменение language baseline и build image —
разные compatibility решения.

## Изменение контрактов

- Публичный route: обновить handler/route contract, OpenAPI schema/examples и
  tests в `repos/gateway/api`.
- Environment variable: обновить code default, Helm values/template,
  `configuration.md` и component README.
- Remote module DTO: обновить обе стороны, contract tests и data-boundary
  sections в architecture/security docs.
- SQL migration: обновить source migration и embedded Helm ConfigMap copy.
- UI route: обновить route manifest, navigation semantic icon и frontend tests.

Не добавляйте внутренний `RequestContext` в wire contract и не логируйте
secrets для упрощения диагностики.

## Проверки

Полный набор команд приведён в [operations](operations.md). Минимум для
изменённого Go module:

```bash
gofmt -w .
go vet ./...
go test ./...
go build ./...
```

Concurrency-sensitive изменения дополнительно требуют `go test -race ./...`.
UI проверяется typecheck, Vitest и production build. Helm templates —
`helm lint`; OpenAPI — schema, route coverage и compatibility checks. Gateway
API tests также проверяют внутренние Markdown links, компактность корневого
README и соответствие source migrations их Helm ConfigMap copies.

## CI и releases

`.github/workflows/test.yml` проверяет OpenAPI, каждый Go module и UI. OpenAPI
PR diff сравнивается с точным base commit и блокирует definite/potential
breaking changes.

`.github/workflows/release.yml` собирает отдельные images компонентов и
публикует release artifacts по принятой tag policy. Runtime image не должен
содержать исходники, build tools или credentials.

При миграции со старого combined Helm release сначала обновите `ai-gateway`,
чтобы он удалил ранее принадлежавшие ему module resources, затем установите
отдельные service charts. Helm не принимает объект, уже принадлежащий другому
release.
