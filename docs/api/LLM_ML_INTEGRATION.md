# Контракт бэк → ML → бэк

Все операции LLM принадлежат бэку. ML читает готовые события и возвращает Match.
В репозитории ML не менялся. Контракт событий основан на ML v0.1, метаданные
обработки находятся снаружи `event`, чтобы не нарушать `extra=forbid` в ML.

## Чтение

Внутренний gRPC: `alfa.v1.EventService/ListPreparedEvents` и `GetPreparedEvent`.
Определения находятся в `contracts/proto/v1/backend.proto`; JSON-ответ — в
`Response.payload` (UTF-8). Для списка задаются `Request.company_id`, `limit`
(1–100), `offset`; для одного события — также `Request.id`.

Список возвращает `{items: Prepared[], limit, offset}`. Данные для ML находятся
в `Prepared.event`; рядом — `schema_version=backend-llm-v1`, `event_version`,
`input_revision`, `scope`, версии промпта/справочника/провайдера, `synthetic`.
Версия справочника: `ml-stage1-v1`. Список секторов/факторов закреплён в
`contracts/events/llm.schema.json` и взят из входящих ML-справочников.

`event` содержит `event_id,title,summary,sources,type,sectors,okved,companies,
regions,factors,direction,horizon,importance,collected_at`.
`sources` содержит URL, название, дату публикации и `kind=news`.
При отсутствии источника с датой событие не считается подготовленным.
`input_revision` обозначает входную версию кандидата, `event_version` — версию
после сохранения результата. В Match возвращать именно `event_version`.

Готовность сообщается через `events.lifecycle.v1`, тип `event.tagged`.
В уведомлении только ссылка и версия; снимок читается через gRPC. Kafka даёт
повторы, поэтому ключ обработки — `(event_id,event_version,profile_version)`.
Периодическое перечитывание восстанавливает пропущенные уведомления. Offset —
пагинация текущего списка, а не курсор гарантированной доставки. Повторный обход
и дедупликация обязательны. Публичные и приватные события сохраняют `scope`.

## Запись Match

`alfa.v1.InsightService/SubmitMatch`, определения в `pipeline.proto`.
`Request.company_id` совпадает с `payload.company_id` и подписанным контекстом.
JSON в `payload`:

```json
{
  "company_id": "company-metal",
  "event_id": "<event_id>",
  "profile_version": 1,
  "event_version": 2,
  "ruleset_version": "ml-stage1-v1",
  "score": 0.5,
  "breakdown": [
    {"channel":"demand","weight":0.7,"match":0.5,"contribution":0.35}
  ],
  "synthetic": false
}
```

Числа в примере иллюстративные. Все значения — конечные числа в `[0,1]`.
`contribution` соответствует `weight * match` с допуском 0.001.
Требуется хотя бы один канал. Допускаются машинные имена
`industry/direct_industry,competitor,demand,supply,financial,currency/fx_ved,
commodity,regional,regulatory`.

Проверяется актуальность обеих версий. При изменении снимка возвращается
`FAILED_PRECONDITION`; нужно перечитать данные и пересчитать Match.
Одна запись соответствует компании, событию и двум версиям; повтор записи
обновляет Match этой комбинации. Уже созданная карточка сохраняет свой снимок.
Внешний импорт принимает `synthetic=false`; мок-оценки создаются отдельным
адаптером `MATCH_MODE=mock`. Для реальной интеграции включить `MATCH_MODE=external`:
без подходящего Match карточка создаваться не будет.

## Подписанный контекст

Cookie пользователя для ML не нужна. На каждый RPC формируется новый токен.
Metadata: `authorization: Bearer <token>`.

Claims: `sub=ml-service`, `service=true`, `company_id=<обрабатываемая компания>`,
`aud=event-service` для чтения или `aud=insight-service` для записи,
`exp=текущее Unix-время + 60`, `correlation_id=<ID обработки>`.
Для Company Service используется `aud=company-service`.

Подпись MVP: `base64url(JSON claims)` без `=` + `.` +
`base64url(HMAC-SHA256(INTERNAL_KEY, encoded_claims))` без `=`.
Ключ передаётся через окружение доверенного процесса, не через JSON профиля,
фронт, Git или логи. Для другой компании/получателя требуется новый токен.
Запись Match доступна сервисным субъектам `ml-service`, `ranking-service`,
`scoring-service`. `AIService.Run` для них закрыт: готовый результат уже на бэке.

Ограничение текущего MVP: общий внутренний HMAC-ключ, доверенная внутренняя сеть;
раздельные сервисные ключи и mTLS остаются последующим этапом.


## Подготовка интерпретаций профиля

После импорта можно вызвать `AIService.QueueProfile` с Request.company_id.
Сервисные субъекты: `ml-service`, `company-service`, `insight-service`; компания
обязательна в подписанном контексте. Данные и признаки AI Service читает сам из
Company Service. Payload с произвольными фактами клиента не используется.

Ответ — сохраняемое задание `kind=profile`; его состояние читается через
`AIService.GetProfileJob`. В ready находится Completion, в `result.output` —
ProfileBrief с company_id, profile_version, summary, observations, hypotheses,
limitations. Наблюдения/гипотезы ссылаются на существующие feature_ids. Числовые
Company/Profile не меняются. Повторный профиль переиспользуется между новостями.

Для списка компаний нужно перебрать ID импортированной выборки; логика одинакова
для всех. Карточка при отсутствии кеша строит такую интерпретацию самостоятельно.
В Prepared добавлен workflow_id; внутренний Event по-прежнему совместим с v0.1.
