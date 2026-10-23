# ML-контур: персонализированная новостная аналитика (Альфа Хак)

Python-пакет `ml/` для разделов 7–13 ТЗ: тегирование событий (A), ранжирование (B), RAG по аналитике банка (C),
логика AI Agent с самопроверкой (D, E), оценка качества (F), построение профиля (G).
Всё работает без LLM-ключа и без сети в режиме `LLM_MODE=mock` (по умолчанию).

## Быстрый старт

```powershell
cd ml
pip install -r requirements.txt
python -m pytest                                  # 112 тестов, без сети и ключа
python scripts/ingest_bank_docs.py                # документы банка -> data/bank_chunks.jsonl
python scripts/demo_correction.py                 # демо коррекции агента (A-05) с трассой
python scripts/run_eval.py                        # метрики на фикстурном golden_set
python scripts/warm_cache.py --n 5                # прогрев кеша карточек
python scripts/budget_report.py --warm-cards 100  # токены, Cost per Insight
uvicorn ml.services.agent_app:app --port 8003     # ещё: ranking_app (8001), knowledge_app (8002), tagging_app (8004)
```
(для uvicorn нужен `PYTHONPATH=src` или запуск из каталога `src`).

## Переменные окружения

| Переменная | Назначение |
|---|---|
| `LLM_MODE` | `mock` (по умолчанию) или `http` |
| `LLM_API_KEY` | ключ API, только из окружения; в коде, логах и конфиге не хранится |
| `LLM_BASE_URL` | адрес OpenAI-совместимого API (`POST {base}/chat/completions`) — `TODO(align)` |
| `DATA_DIR` | каталог с `df_Uid_data_hack.csv`, `df_Uid_payment_hack.csv`, `df_Fid_Uid_link_hack.csv`, `bank_analytics/` |
| `PROFILE_SERVICE_URL`, `EVENT_SERVICE_URL`, `NEWS_SERVICE_URL`, `KNOWLEDGE_SERVICE_URL` | HTTP-клиенты портов; не заданы — фикстурные заглушки из `tests/fixtures/` |
| `COMPANY_SERVICE_ADDR` | адрес gRPC Company Service (по умолчанию `company-service:9090`) |
| `COMPANY_SIGNED_CONTEXT` | «подписанный контекст» для Company Service, только из окружения, в логи не пишется; `TODO(align)`: что это и в каком заголовке слать (`company_service.signed_context_header`) неизвестно |
| `ML_AS_OF` | «текущий момент» для свежести (по умолчанию: реальное время; для фикстурных новостей — дата последней новости) |

## Пример запросов

```bash
curl -X POST localhost:8001/rank -H 'content-type: application/json' \
  -d '{"client_id":"DEMO_1","events":[{"event_id":"e1","title":"...","type":"макро","importance":4,"sectors":["Строительство"],"published_at":"2026-09-02T09:00:00+03:00"}],"top_n":5}'
curl -X POST localhost:8002/search -d '{"query":"ключевая ставка 14%","k":3}' -H 'content-type: application/json'
curl -X POST localhost:8003/analyze -d '{"client_id":"DEMO_1","event_id":"evt_003","role":"CEO"}' -H 'content-type: application/json'
curl -X POST localhost:8003/analyze_text -d '{"client_id":"DEMO_1","role":"CFO","title":"...","text":"..."}' -H 'content-type: application/json'
```
Заголовок `X-Auth-Client-Id` (его выставляет Gateway) при несовпадении с `client_id` даёт 403.

## Структура

`src/ml/`: `schemas.py` (контракт, см. `CHANGELOG.md`), `models.py` (локальные типы API), `llm/` (клиент, кеш, бюджет, mock),
`tagging/`, `ranking/`, `knowledge/`, `agent/`, `validate.py`, `evaluation/`, `profile/`, `ports.py`, `services/`, `prompts/` (версии = имена файлов).
Настройки: `config/config.yaml` (веса, пороги, модели), справочники в `config/`.

## Решения, которые стоит знать

- **Хранилище** — один SQLite-файл (`.cache/ml_store.sqlite`): кеш LLM (`sha256(model, messages)`), теги (по `event_id`), карточки (по `client|event|role|prompt_version`), трассы, журнал вызовов, дневной расход.
- **Бюджет**: `normal` → `degraded` (≥ `llm.degrade_at` = 80% от `daily_limit_rub`: только кеш, вызов без кеша даёт `DegradedMode`) → `BudgetExceeded` (≥ 100%). Расход считается по журналу за сегодня; цены токенов пока не заданы (`TODO(align)`), поэтому стоимость 0. Ключи `cheap_model` и `cache_only_at` в `config.yaml` относятся к прежней схеме (дешёвая модель с 80%, только кеш с 95%) и не используются.
- **Покрытие платежей** (8.3): порог «только платежи» взят из конфига (0,50), как в разделе 18; формулировка раздела 8.3 («≥ 0,30») ей противоречит.
- **Числа в карточке**: сравниваются как числа (14,0 = 14), целые 1–10 без «%» считаются счётом, утверждения со словом «оценка» освобождены.
- **Тегирование**: один вызов LLM на событие, кеш по `event_id` (SQLite, таблица `kv`); пустой, обрезанный и не русский текст помечается шумом (`is_noise`, причина в `noise_reason`), но тоже тегируется и не удаляется. При сбое разбора ответа модели `event = null`, ничего не кешируется.
- **Эмбеддинги не используются**: поиск — BM25 (`knowledge/retriever.py`), подмена через интерфейс `Retriever`.

## LLM-клиент (шаг 1 пятничного ТЗ)

`src/ml/llm/`: `base.py` (порт `LLMClient.complete(task, messages, schema=None, run_id=None, *, prompt_version=None)`, кеш, журнал, `Budget`),
`mock.py` (`MockLLM`, сценарий `[INVALID, None]` = «первый ответ невалидный, второй валидный»), `http.py` (`HttpLLM`), `build_llm(cfg)`.
Кеш (`sha256(model + messages)`) и журнал вызовов лежат в SQLite по `paths.store`. Невалидный JSON: один повтор с текстом ошибки,
затем `LLMFormatError`; сбой сети: до `llm.net_retries` (2) повторов. Бюджет шага 1: `normal` → `degraded` (≥ `llm.degrade_at` = 80%:
только кеш, вызов без кеша даёт `DegradedMode`) → `BudgetExceeded` (≥ 100%). Цены (`llm.prices`) пусты: стоимость 0 и предупреждение
`TODO(align)`, пока их нет, бюджет не расходуется. Проверка доступа: `LLM_MODE=mock python scripts/check_llm.py`.
Ключи `cheap_model` и `cache_only_at` в `config.yaml` и описание бюджета выше («Решения») относятся к прежней схеме и клиентом шага 1 не используются.

## Тегирование событий (шаг 2 пятничного ТЗ, контракт v0.1)

`src/ml/contract.py` — командный контракт данных v0.1 (копия `schemas.py` из `contract_v0.1`, без изменений; главный). `src/ml/schemas.py` (приложение А) не используется
тегированием и остаётся для кода профилей. `src/ml/tagging/`: `guard.py` (защита от инъекций), `aliases.py` (компании -> сектор), `tagger.py` (`Tagger.tag_event`),
`mock_rules.py` (правила для ответов в режиме mock). Промпт: `src/ml/prompts/tagging-v2.txt` (версия `tagging-v2`).
`Event` собирается так: `event_id`, `title` из входа (после очистки), `sources = [SourceRef(url, name=source, published_at, kind=news)]`, `collected_at` = момент вызова,
остальное из ответа модели; без корректного `url` Event не строится (`event = null`, `noise_reason = "нет источника"`, LLM не вызывается).
Значения `type`, `direction`, `horizon` — из контракта, `sectors` — из `config/sectors.yaml`, `factors` — идентификаторы из `config/factor_ids.yaml`, `okved` — двузначные коды; прочее отбрасывается с записью в лог.

```bash
LLM_MODE=mock python scripts/run_tagging.py --input tests/fixtures/news_30.json      # таблица, итоги, out/tags.jsonl
uvicorn ml.services.tagging_app:app --port 8101                                      # из каталога src или с PYTHONPATH=src
curl -X POST localhost:8101/tag -H 'content-type: application/json' -d '{"event_id":"e1","title":"...","text":"...","published_at":"2026-10-20T10:00:00+03:00","source":"Издание","url":"https://..."}'
```
Ответ `/tag` и элементов `/tag/batch` (тело и ответ — массивы): `{"event": Event | null, "is_noise": bool, "noise_reason": str | null, "suspicious": bool}`; `503`, если LLM недоступен или исчерпан бюджет.

## Формат Company Service

Профиль в Company Service (`libs/go/model/model.go`): `company_id, version, ruleset_version, channels[{name, sector, weight, source, evidence[]}], factor_exposure{}, coverage, built_at, synthetic`; реквизиты, ОКВЭД, продукты лежат в отдельном документе Company. Адаптер: `src/ml/company_format.py` (`adapt_profile`, `export_profile`, `export_company`); экспорт: `python scripts/export_profiles.py --out-dir DIR` (profiles.json для `ReplaceProfile`, companies.csv для импорта). `version` в хранилище должен быть last+1. В Company Service не хранятся buyers, suppliers, concentration, commodities, а канала «аффилированный» нет; факторы (`factor_exposure`) пока пусты. Список расхождений: `TODO(align)` в `company_format.py`.

## Выгрузка профилей

**1. Получить `profiles.json`.** Профили строятся из платежей и справочника клиентов, затем экспортируются в формат Company Service:
```
python scripts/build_profiles.py --data-dir <DATA_DIR>          # -> data/profiles.json (профили ML)
python scripts/export_profiles.py --out-dir export_company_service
```
Экспорт даёт `profiles.json` (список профилей для `ReplaceProfile`), `companies.json` (все 7 продуктов) и `companies.csv` (CSV-импорт знает только loan, leasing, deposit). `features` в `profiles.json` попадает только при `export.include_features: true` (см. «Поле features профиля»).

**2. Загрузить в Company Service** (gRPC, `alfa.v1.CompanyService`: `GetProfile`, затем `ReplaceProfile` с версией last+1):
```
python scripts/push_profiles.py                       # dry-run (по умолчанию): читает GetProfile, ничего не записывает, печатает план
python scripts/push_profiles.py --apply               # реальная отправка ReplaceProfile
python scripts/push_profiles.py --src FILE --address host:port
```
- Адрес: `COMPANY_SERVICE_ADDR` или `company_service.address` (по умолчанию `company-service:9090`).
- Профиль, который не отличается от сохранённого (без учёта `version` и `built_at`), пропускается: повторный запуск не плодит версии. Ошибка по одному клиенту не останавливает остальных; при ошибках код возврата 1. Вывод ограничен 50 строками.
- Скрипт уважает `export.include_features`: при `false` ключ `features` удаляется из отправляемого профиля, даже если он есть в файле (`--include-features` включает отправку). Пока бэкенд хранит профиль без `features`, поле при сравнении не учитывается.
- Тесты используют заглушку gRPC-сервера в памяти; реальный сервис не вызывается.

**Что открыто (`TODO(align)`)**
- «Подписанный контекст»: что это, как получить и в каком metadata-заголовке слать — не известно. Значение берётся из `COMPANY_SIGNED_CONTEXT`, имя заголовка — `company_service.signed_context_header` (пусто — контекст не отправляется). Без него реальный `ReplaceProfile` может быть отклонён (проверено только на заглушке).
- Поле `features`: в `model.Profile`, OpenAPI и `ReadImport` его нет, поэтому выгрузка выключена флагом. Названия полей согласованы по примеру бэкенда (доли секторов, `top1_share`, `top5_share`, `hhi`), но фактически не проверены на реальном сервисе.
- TLS для gRPC-канала не согласован (канал без шифрования).
- `ruleset_version` (`ml-stage1-v1`), `name` компании (= `client_id`), `factor_exposure` (пока `{}`), coverage одним числом (минимум по сторонам).

## Поле features профиля
`features` = `{"buyers": {...}|null, "suppliers": {...}|null}`: у стороны `sector_shares` [{sector, share}] и `concentration` {top1_share, top5_share, hhi}. Расчёт: `src/ml/profile/features.py`; сырьё и coverage в features не входят. Округление (решение согласовано с бэкендом): `sector_shares.share` - 3 знака, `top1_share`/`top5_share`/`hhi` - 4 знака.

Флаг `export.include_features` (config.yaml) по умолчанию `false`: тогда `features` не попадает ни в `profiles.json`, ни в payload `ReplaceProfile` (профиль без ключа). Причина: бэкенд пока не знает поле (`model.Profile`, OpenAPI, `ReadImport`): gRPC молча его отбросит, импорт отвергнет. Расчёт features остаётся, просто не выгружается. Включить: `true` в конфиге или `--include-features` у `export_profiles.py`.

## Пороги профиля

Значения из `config/config.yaml`:
- `profile.revenue_threshold` = 15 млрд руб. — в профили попадают клиенты с выручкой не ниже порога.
- `profile.concentration_min_counterparties` = 5 (строк в таблице платежей = контрагенты, `PAY_CNT` не используется) и `profile.concentration_min_coverage` = 0.3 — концентрация стороны считается при обоих условиях.
- Каналы «спрос»/«предложение» по покрытию платежей (`ranking.coverage_*`): < 0.3 — только матрица секторов; 0.3–0.5 — платежи и матрица (берётся большее); ≥ 0.5 — только платежи.

## Что в репозитории НЕ настоящее (важно)

- `tests/fixtures/bank_analytics/*.txt` — **иллюстративные тексты** под названия документов из приложения 2, не аналитика банка. Реальные PDF положить в `$DATA_DIR/bank_analytics/` и запустить `ingest_bank_docs.py`.
- `tests/fixtures/profiles_demo.json` — 5 вымышленных клиентов `DEMO_1..5`. Настоящие профили: `python scripts/build_profiles.py` на реальном `DATA_DIR`.
- `tests/fixtures/golden_set.csv` — **не ручная разметка**: метки ставит простой скрипт-оракул (`scripts/make_golden_fixture.py`), нужен только чтобы `run_eval.py` было что считать. Для реальной оценки: `scripts/make_golden_template.py`, разметка людьми.
- `config/sector_matrix.csv`, `sectors.yaml`, `aliases.yaml`, `sector_commodities.csv` — **черновики, требуют ревью человеком** (Андрей/ML); aliases заменить на 137 компаний из `players_by_sector.xlsx`.
- Mock-LLM даёт реалистичный по форме, но шаблонный текст; качество формулировок настоящих моделей не проверено.
- Реальные данные кейса (`DATA_DIR`) при разработке были недоступны: построитель профилей проверен на синтетике (`scripts/make_synthetic_data.py`), приёмка «115 компаний» и `U_5658` на реальных данных не выполнена.

## Метрики на фикстурах (mock, dev-split)

Precision@5 ≈ 0,8; hit@3 для RAG = 1,0; утверждений с источником 100%. Пересечение лент 0,38 (цель ≤ 0,3, **не достигнута**: у клиентов с кредитом/лизингом в топе общие новости про ставку). Recall@5 и доля косвенных связей **ниже цели** (≈0,29 и ≈0,38): в пуле из 30 событий у клиента ~14 значимых, а K=5, поэтому Recall@5 не может превысить 5/14 — это свойство фикстуры, а не оценка качества. Цифры на реальной разметке будут другими.
