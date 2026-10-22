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
- **Бюджет**: `normal` → `degraded` (≥ 80%: дешёвая модель, N карточек уменьшается до `degraded_top_n`) → `cache_only` (≥ 95%) → отказ `BudgetExceeded` без записи в кеше (агент возвращает `status=budget_exceeded`).
- **Покрытие платежей** (8.3): порог «только платежи» взят из конфига (0,50), как в разделе 18; формулировка раздела 8.3 («≥ 0,30») ей противоречит.
- **Числа в карточке**: сравниваются как числа (14,0 = 14), целые 1–10 без «%» считаются счётом, утверждения со словом «оценка» освобождены.
- **Тегирование без LLM** для пустого, обрезанного и не русского текста (экономия бюджета); при сбое LLM — запасные правила (`tagging/heuristics.py`), результат не кешируется.
- **Эмбеддинги не используются**: поиск — BM25 (`knowledge/retriever.py`), подмена через интерфейс `Retriever`.

## Формат Company Service

Профиль в Company Service (`libs/go/model/model.go`): `company_id, version, ruleset_version, channels[{name, sector, weight, source, evidence[]}], factor_exposure{}, coverage, built_at, synthetic`; реквизиты, ОКВЭД, продукты лежат в отдельном документе Company. Адаптер: `src/ml/company_format.py` (`adapt_profile`, `export_profile`, `export_company`); экспорт: `python scripts/export_profiles.py --out-dir DIR` (profiles.json для `ReplaceProfile`, companies.csv для импорта). `version` в хранилище должен быть last+1. В Company Service не хранятся buyers, suppliers, concentration, commodities, а канала «аффилированный» нет; факторы (`factor_exposure`) пока пусты. Список расхождений: `TODO(align)` в `company_format.py`.

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
