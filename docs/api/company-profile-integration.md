# Company Service: подключение ML-профилей

Продолжение существующего Company Service из `develop`. Контракт gRPC не меняется:
`GetCompany`, `GetProfile`, `ReplaceProfile` по `contracts/proto/v1/backend.proto`.

## Изменения

- `Profile.features` сохраняется и возвращается через импорт, PostgreSQL и gRPC.
- Структура соответствует `ml/docs/profile_features_schema.md` из ветки
  `feature/ml-stage1-profiles` (f9643d8): `buyers`, `suppliers`, `sector_shares`, `concentration`.
- CSV-импорт поддерживает семь продуктов: `rko`, `guarantee`, `loan`,
  `leasing`, `deposit`, `zpp`, `factoring`. Пустое значение остаётся `null`.
- `ReplaceProfile` отклоняет неизвестные JSON-поля, включая вложенные, и лишний JSON
  после документа. Прежнее молчаливое удаление `features` устранено.
- Проверяются конечность и диапазоны долей, дубли секторов, размер top-10,
  согласованность концентрации. Допуск суммы округлённых долей: 0.0005 на элемент.

Старые профили без `features` принимаются. Отсутствующий объект не появляется
в ответе. Неизвестная сторона или концентрация — `null`, а не нули. Доли top-10
могут суммироваться меньше единицы. Это агрегаты, а не индивидуальные платежи.
Сырьё и coverage по сторонам в этот контракт не добавлены.

## Экспорт и импорт

В рабочей копии ML-ветки выгрузить профиль с features:

```sh
python ml/scripts/export_profiles.py --src /path/to/ml-profiles.json --out-dir /path/to/export --include-features
```

ML-экспортёр пока пишет только три продукта в CSV, хотя все семь есть в
`companies.json`. Новый адаптер восстанавливает полный CSV без изменения ML-ветки:

```sh
python tools/data/export_company_import.py --companies-json /path/to/export/companies.json --output /path/to/export/companies.csv
go run ./services/company-service/cmd/app import --companies /path/to/export/companies.csv --profiles /path/to/export/profiles.json --dry-run
```

`--dry-run` не требует PostgreSQL. Тот же импорт без `--dry-run` записывает пакет
атомарно в настроенную БД сервиса. Ограничения пакета: 10 000 компаний и 10 MiB
на каждый файл. Версия нового профиля — последняя сохранённая + 1.
Повтор идентичного пакета идемпотентен. Изменение с прежней версией даёт конфликт.

Обновление gRPC требует подписанных claims: `aud=company-service`,
`company_id` совпадает с запросом и документом, `service=true`.
Metadata: `authorization: Bearer <token>`. Формат подписи определён в
`libs/go/platform/security.go`. Секреты и токены не входят в примеры.

## Кеш и границы этапа

Company Service хранит версии рассчитанных профилей в PostgreSQL, отдаёт последнюю
и не вызывает LLM при чтении. Кеш вычислений остаётся в ML-ветке. Новый кеш в памяти
не добавлен: после замены версии нет отдельной проблемы инвалидации.

Это передача готовых признаков. Реализация ML-правил PR-01/02/03/06, LLM-fallback
PR-04 и скоринга F2 не дублируется. Они не объявляются выполненными только на
основании наличия поля в API. Реальные профили не включаются в Git.

В `tools/data` перенесён анализ D-01/D-03/D-04: покрытие платежами по ОКВЭД и уровни
**раздел / класс / группа**. Денежные единицы CSV не установлены. ML-экспортёр
умножает выручку на 100 по собственному допущению; этот этап сохраняет значение,
но не подтверждает допущение. Импорт данных не означает проверку ML-эвристик.

## Проверки

```sh
go test ./services/company-service/... ./libs/go/model/...
python scripts/check-contracts.py
python -m unittest discover -s tools/data -v
```

gRPC-тест использует настоящий клиент, сервер, проверку подписанных claims и
хранилище в памяти. PostgreSQL-тест проверяет импорт, повторный импорт, чтение и
замену `features`; для него требуется `TEST_DATABASE_URL`. Он создаёт собственную
временную БД и не очищает переданную базу.

Проверено 23.10.2026: тесты Company Service и API Gateway, восемь тестов обработки
данных и адаптера, проверка контрактов, dry-run импорта 115 профилей, построенных
ML-веткой на трёх предоставленных CSV. PostgreSQL-тесты пропущены: локальная БД
не запущена и `TEST_DATABASE_URL` не задан. Запись реальных профилей в БД не выполнялась.
