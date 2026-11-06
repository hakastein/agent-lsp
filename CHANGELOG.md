# Changelog

## v0.1.0

- Модуль `github.com/hakastein/agent-lsp` — Go-библиотека: программа импортирует её и спрашивает напрямую, без
  LSP-сервера, демона и подпроцессов.
- API общий для языков: корневой пакет `agentlsp` — позиции, места, ответы и `ErrNotSupported`; пакет `php` —
  `Open(root, logf)` и индекс с методами `Definition`, `References`, `Rename`, `Symbols`, `Outline`, `Close`.
- PHP: `Definition` и `References` методов с известным классом получателя (правила — ADR 0001). `References`
  отдаёт использования в `Uses`; `Rename`, `Symbols` и `Outline` возвращают `ErrNotSupported`.
- Позиция — абсолютный путь, строка и колонка в байтах, обе с 1.
- SQLite — `modernc.org/sqlite` через `database/sql`, без CGO. Индекс лежит в
  `<кэш пользователя>/agent-lsp/php/index-v3-<парсер>/`.
- Платформы — linux, darwin и windows.
- Программа, которая импортирует agent-lsp, повторяет в своём `go.mod` строку `replace` парсера PHP.
