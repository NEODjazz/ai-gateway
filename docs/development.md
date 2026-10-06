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

Sidebar объединяет доступные страницы в блоки и раскрывающиеся подменю.
При ширине окна до 767 px он автоматически переходит в компактный режим;
подменю доступны через кнопки с иконками и с клавиатуры. Возврат к широкому
окну восстанавливает выбранный пользователем режим панели. При проверке UI
проверяйте отсутствие горизонтального переполнения страницы в каждом режиме
Playground, раскрытие подменю и возврат фокуса после Escape.

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

[CI workflow](../.github/workflows/test.yml) проверяет OpenAPI, шесть Go modules
(vet, race tests, coverage), React typecheck/tests/build и production dependency
audit, восемь Helm charts и сборку шести Go images. OpenAPI PR diff сравнивается
с точным base commit и блокирует definite/potential breaking changes.

Обязательные отдельные jobs используют реальные PostgreSQL databases для
control plane, identities и billing, ClickHouse для organization reporting,
Keycloak/OpenWebUI для identity integration и Docling/Redis/workers для
conversion/OCR, admission и owner isolation. Локальный `go test ./...` без DSN
не заменяет эти сценарии: opt-in integration tests могут быть skipped.
[Проверка Lemonade](lemonade-validation.md) остаётся opt-in с реальным сервером;
наличие локального сервера не является требованием unit tests.

`.github/workflows/release.yml` собирает отдельные images компонентов и
публикует release artifacts по принятой tag policy. Runtime image не должен
содержать исходники, build tools или credentials.

При миграции со старого combined Helm release сначала обновите `ai-gateway`,
чтобы он удалил ранее принадлежавшие ему module resources, затем установите
отдельные service charts. Helm не принимает объект, уже принадлежащий другому
release.
