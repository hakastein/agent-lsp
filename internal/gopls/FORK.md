# Форк gopls

Каталог `internal/gopls` — копия внутренних пакетов gopls и `golang.org/x/tools`, которые пакет `golang` вызывает
напрямую. Код принадлежит The Go Authors и распространяется по лицензии BSD-3 ([LICENSE](LICENSE),
[PATENTS](PATENTS)); каждый файл сохраняет заголовок авторов.

## Версия upstream

| Модуль | Версия | Коммит |
|---|---|---|
| `golang.org/x/tools/gopls` | v0.23.0 — последний выпуск с `go 1.26` в `go.mod` | `014f87ff5c01915bc90f4f11a6bb8aea3e0edbd7` |
| `golang.org/x/tools` | v0.47.1-0.20260707181000-a299dadba899 — из `go.mod` gopls v0.23.0 | `a299dadba899` |

Версия форка записана и в константе `forkVersion` пакета `golang`: она входит в ключи кэша, поэтому любая правка
форка, которая меняет содержимое кэша, меняет и её.

## Раскладка

| Upstream | Форк |
|---|---|
| `golang.org/x/tools/gopls/internal/<p>` | `internal/gopls/<p>` |
| `golang.org/x/tools/internal/<p>` | `internal/gopls/xtools/<p>` |
| `golang.org/x/tools/go/packages` | `internal/gopls/xtools/go/packages` |

- Импорты переписаны по этой таблице, затем `gofmt -w`: порядок импортов в группе меняется вместе с путями.
- Не копируются тесты (`_test.go`), `testdata` и генераторы (файлы с `//go:build ignore`).
- В форке только пакеты, которые импортирует пакет `golang` (`go list -deps ./golang`). Остальные публичные пакеты
  `golang.org/x/tools` (`go/ast/inspector`, `go/types/objectpath` и т.п.) — зависимость `go.mod` той версии, что
  выберет сборка (не ниже записанной там): их API совместим между версиями, а закрепить старую версию нельзя —
  потребитель с более новой `x/tools` всё равно поднимет её.
- `go/packages` скопирован, хотя он публичный: он делит состояние с `internal/packagesinternal` (функцию
  `GetDepsErrors`), а копия `packagesinternal` из форка с пакетом из модуля это состояние не делит.

## Правки

Удалённые файлы — файлы протокола LSP, анализаторов, vulncheck и операций, которых agent-lsp не вызывает:

- `cache/analysis.go` (драйвер анализаторов), `cache/mod_vuln.go` (govulncheck);
- `settings/analysis.go`, `settings/staticcheck.go` (списки анализаторов и staticcheck);
- `protocol/context.go`, `protocol/log.go`, `protocol/protocol.go`, `protocol/tsclient.go`, `protocol/tsserver.go`
  (JSON-RPC клиент и сервер);
- в `golang` оставлены только `comment.go`, `definition.go`, `embeddirective.go`, `identifier.go`,
  `implementation.go`, `linkname.go`, `origin.go`, `references.go`, `snapshot.go`, `util.go`: замыкание
  `Definition`; остальные файлы пакета не копируются.

Новые файлы форка, с копиями кода из удалённых файлов без изменений:

- `cache/fork_gob.go` — типы сериализации диагностик из `cache/analysis.go`;
- `golang/fork_helpers.go` — `parsePackageNameDecl` (`rename.go`), `docLinkRegex`, `findDocLinkIndices`,
  `isScheme`, `is` (`semtok.go`), `trimVersionSuffix` (`stub.go`), `cond` (`extract.go`);
- `protocol/fork.go` — `UnmarshalJSON` и `NonNilSlice` из `protocol/protocol.go`;
- `xtools/go/packages/fork.go` — `SetDepsErrors`: восстановленный из базы пакет получает ошибки зависимостей.

Изменённые файлы:

- `filecache/filecache.go` переписан: кэш — таблица SQLite (`Open`, `Flush`, `Stats`), `Set` копит значения в памяти
  до `Flush`, ключ строки включает соль; `GetOrFatal` не завершает процесс, ошибка чтения — промах. Сборщика мусора
  по файлам нет: старые строки удаляет `internal/cachedb`.
- `cache/view.go`: поле `Folder.Load` — замена `packages.Load` для загрузок папки.
- `cache/load.go`: `Snapshot.load` вызывает `Folder.Load`, если оно задано.
- `cache/errors.go`: удалена `toSourceDiagnostic` (нужна только `cache/analysis.go`).
- `settings/settings.go`: `CounterPath` — свой тип `[]string` вместо `telemetry.CounterPath` (без телеметрии).
- `util/bug/bug.go`: без счётчика телеметрии `BugReportCount`.

## Как подтянуть upstream

1. Выбрать выпуск gopls, чей `go.mod` требует версию Go не новее `go.mod` agent-lsp, и его версию `x/tools`.
2. Скопировать по таблице раскладки пакеты, которые есть в форке, переписать импорты, `gofmt -w`.
3. Удалить файлы из списка удалённых, вернуть новые файлы форка и правки изменённых файлов; копии в новых файлах
   сверить с их источниками в upstream.
4. Собрать `go build ./...`: неопределённое имя в `golang` — либо файл upstream, который стал нужен (добавить в
   список), либо помощник, переехавший в другой файл (поправить `fork_helpers.go`). Пакет, которого больше нет в
   `go list -deps ./golang`, удалить.
5. Обновить таблицу версий, `forkVersion` и этот список правок; `make go`.
