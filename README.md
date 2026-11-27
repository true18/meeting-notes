# Meeting Notes

«Умный помощник для конспектирования встреч» — CLI-приложение на Go. Оно сохраняет загруженные материалы, ставит обработку в durable-очередь PostgreSQL, получает транскрипцию через `SpeechClient`, строит выжимку через `LLMClient` и предоставляет список встреч, статусы, поиск и вопросы по собственным материалам пользователя.

По умолчанию используются детерминированные mock-клиенты: проект полностью демонстрируется без внешних AI API и не выдаёт mock за реальное распознавание или нейросетевую суммаризацию.

## Архитектура

```text
CLI -> app.Service -> PostgreSQL repository / file storage / LLM
                           ^
                           |
                 worker -> Speech -> LLM
```

- `cmd/meetings` — сборка зависимостей, сигналы и exit codes;
- `internal/cli` — разбор команд и вывод без SQL;
- `internal/app` — пользовательские сценарии;
- `internal/repository/postgres` — параметризованные SQL-запросы и транзакции pgx;
- `internal/storage` — потоковое атомарное копирование файлов через `os.Root`; операции root-relative ограничены управляемым каталогом и не позволяют переходить по `..` или внешним symlink. Это ограничение файловых операций через данный root, а не песочница всего процесса;
- `internal/speech` и `internal/llm` — небольшие интерфейсы провайдеров;
- `internal/worker` — ограниченный пул фоновой обработки;
- `migrations` — встроенные SQL-миграции.

CLI выбран как основной интерфейс по условию проекта: он воспроизводим, не требует аккаунта в мессенджере и позволяет отдельно показать клиентские команды и долгоживущий worker.

## Требования и установка

- Go 1.24 или новее;
- PostgreSQL 14 или новее;
- Docker Compose необязателен, но упрощает локальный запуск.

```bash
git clone <repository-url> meeting-notes
cd meeting-notes
go mod download
docker compose up -d postgres
export DATABASE_DSN='postgres://postgres:postgres@localhost:5432/meeting_notes?sslmode=disable'
go run ./cmd/meetings migrate
```

Remote для этого локального проекта пока не настроен; после создания репозитория замените `<repository-url>` фактическим адресом. `compose.yaml` содержит только демонстрационные локальные credentials.

## Конфигурация

Настройки читаются из окружения. Пример находится в `.env.example`.

| Переменная | По умолчанию | Назначение |
|---|---:|---|
| `DATABASE_DSN` | локальный `meeting_notes` | строка подключения PostgreSQL |
| `STORAGE_DIR` | `./data/uploads` | каталог durable-копий файлов |
| `SPEECH_PROVIDER` | `mock` | реализованный speech provider |
| `LLM_PROVIDER` | `mock` | реализованный LLM provider |
| `WORKERS` | `2` | максимальная параллельная обработка |
| `SPEECH_TIMEOUT` | `30s` | предел одного speech-вызова |
| `LLM_TIMEOUT` | `30s` | предел одного LLM-вызова |
| `PROCESSING_TIMEOUT` | `70s` | предел обработки одной задачи |
| `WORKER_POLL_INTERVAL` | `1s` | пауза при пустой очереди |
| `SHUTDOWN_TIMEOUT` | `15s` | ожидание штатной остановки |
| `DATABASE_TIMEOUT` | `5s` | подключение и обычные CLI-операции |
| `MAX_UPLOAD_SIZE` | `104857600` | максимальный размер файла в байтах |
| `LOG_LEVEL` | `info` | уровень JSON-логов `slog` |

Поддерживаются только фактически реализованные provider-значения `mock`; другое значение считается ошибкой конфигурации.

## Миграции и схема

```bash
go run ./cmd/meetings migrate
```

Команда применяет встроенные `*.up.sql` по порядку и записывает версии в `schema_migrations`.

Основные таблицы:

- `users` — уникальный внешний CLI-идентификатор;
- `meetings` — владелец и метаданные durable-файла;
- `processing_tasks` — состояние фоновой обработки и ошибка;
- `transcripts` — одна транскрипция на встречу;
- `summaries` — одна выжимка на встречу.

Внешние ключи обеспечивают ссылочную целостность. Есть индексы списка пользователя, очереди по статусу и GIN-индексы PostgreSQL full-text search.

Транзакции используются при создании `meeting + processing_task`, сохранении транскрипции с переходом в `transcribed` и сохранении выжимки с переходом в `completed`. Внешние Speech/LLM-вызовы выполняются вне SQL-транзакций.

## Запуск и демонстрация

В первом терминале запустите worker:

```bash
export DATABASE_DSN='postgres://postgres:postgres@localhost:5432/meeting_notes?sslmode=disable'
go run ./cmd/meetings worker
```

Во втором терминале:

```bash
go run ./cmd/meetings -user alice start
go run ./cmd/meetings -user alice load ./testdata/meeting_release.txt
go run ./cmd/meetings -user alice list
go run ./cmd/meetings -user alice status 1
go run ./cmd/meetings -user alice get 1
go run ./cmd/meetings -user alice summary 1
go run ./cmd/meetings -user alice find "релиз"
go run ./cmd/meetings -user alice chat "Кто отвечает за тестирование?"
go run ./cmd/meetings -user alice chat -meeting 1 "Что решили по срокам?"
```

Используйте `meeting_id`, напечатанный `load`: после повторных запусков он может быть не равен 1. `load` выводит `created` и завершается сразу; она не запускает скрытый процесс и не ждёт Speech/LLM.

Неуспешный сценарий:

```bash
go run ./cmd/meetings -user alice load ./testdata/meeting_speech_error.txt
go run ./cmd/meetings -user alice status <meeting_id>
go run ./cmd/meetings -user alice retry <meeting_id>
```

`meeting_llm_error.txt` демонстрирует ошибку после успешной транскрипции.

## Mock Speech и LLM

Mock Speech для `.txt` считает файл заранее подготовленной расшифровкой. Это тестовый режим, а не распознавание аудио. Для произвольного аудиофайла он возвращает понятную ошибку, если для исходного имени нет fixture. Маркеры `[speech:error]` и `[speech:timeout]` используются для проверки отказа и отмены.

Mock LLM возвращает подготовленную выжимку release-сценария или детерминированно извлекает первые содержательные предложения. Для `chat` он выбирает предложение с максимальным пересечением значимых слов вопроса и контекста; если совпадений нет, отвечает, что информация не найдена. Он не придумывает факты. Маркеры `[llm:error]` и `[llm:timeout]` имитируют ошибки.

Реальный провайдер заменяет реализацию интерфейса `speech.Client` или `llm.Client`; app, worker и repository при этом не меняются. Сетевой клиент должен самостоятельно соблюдать переданный context и не логировать секреты.

## Асинхронность и статусы

`load` транзакционно создаёт задачу `created`. Отдельный `worker` запускает ровно `WORKERS` goroutine. Каждая атомарно закрепляет задачу запросом `FOR UPDATE SKIP LOCKED`, быстро фиксирует `processing` и освобождает транзакцию до внешних вызовов.

Рабочий путь: `created -> processing -> transcribed -> completed`. Домен также допускает `summarized`, но отдельный наблюдаемый переход не нужен: выжимка и `completed` фиксируются одной транзакцией. Любая ошибка становится `failed` с текстом и временем. `retry` возвращает только failed-задачу владельца в `created`; прежняя ошибка сохраняется до нового фактического захвата.

При старте worker возвращает в очередь задачи с истёкшим processing lease. `SKIP LOCKED` предотвращает двойную обработку несколькими goroutine или процессами. Poll interval исключает активное вращение пустой очереди.

## Изоляция пользователей и поиск

Все чтения принимают внутренний `user_id`, полученный из внешнего `-user`, и фильтруют SQL одновременно по владельцу и `meeting_id`. `list`, `status`, `get`, `summary`, `find`, `chat` и `retry` не возвращают чужие встречи. Отсутствующая и чужая встреча имеют одинаковую внешнюю ошибку.

`find` использует русскую конфигурацию PostgreSQL full-text search, GIN-индексы, стабильную сортировку и лимит 20; все строки параметризованы. Поэтому запрос «релиз» находит также словоформу «релиза». `chat` получает не более пяти последних транскрипций пользователя по 4000 символов и ограничивает общий контекст 12000 символами.

`-user` — учебная идентификация, а не криптографическая аутентификация: человек, знающий чужой идентификатор, может указать его вручную. Для production нужен отдельный механизм входа.

## Graceful shutdown

`worker` обрабатывает SIGINT, SIGTERM и SIGQUIT через `signal.NotifyContext`. После сигнала новые задачи не захватываются, Speech/LLM получают отмену, а незавершённая задача в отдельном cleanup-контексте возвращается в `created`. Процесс ждёт пул не дольше `SHUTDOWN_TIMEOUT`, затем сообщает timeout. PostgreSQL pool закрывается только после возврата worker.

Логи выводятся JSON-объектами `slog` и содержат `meeting_id`/`task_id`, но не транскрипции, DSN и другие секреты.

## Тестирование

```bash
go test ./...
go test -race ./...
go vet ./...
go test ./... -coverprofile=coverage.out
go tool cover -func=coverage.out
```

Integration-тесты создают и удаляют отдельную схему в явно заданной тестовой БД:

```bash
export TEST_DATABASE_DSN='postgres://postgres:postgres@localhost:5432/meeting_notes_test?sslmode=disable'
go test -v ./internal/repository/postgres
```

Для PostgreSQL из `compose.yaml` тестовую базу можно один раз создать командой `docker compose exec postgres createdb -U postgres meeting_notes_test`.

Если `TEST_DATABASE_DSN` отсутствует, тест явно пропускается. Не направляйте его на production-базу. Unit-тесты проверяют ошибки клиентов, отмену, файловую очистку, CLI-коды и лимит worker concurrency; integration-тест проверяет транзакции, статусы, поиск и изоляцию пользователей.

## Возможные улучшения

- production-аутентификация вместо доверия `-user`;
- реальные Speech/LLM adapters с rate limiting и retry policy;
- heartbeat lease для очень длинной обработки;
- шифрование загруженных файлов и retention policy;
- метрики очереди и tracing;
- пагинация списка и результатов поиска.
