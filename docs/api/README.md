# Контракты бэкенда

Для передачи фронтендеру: [инструкция по интеграции](FRONTEND_INTEGRATION.md)
с последовательностью экранов, JSON-примерами, заголовками и обработкой ошибок.

[Подключение ML-профилей и поля features](company-profile-integration.md).

OpenAPI 3.1: `contracts/openapi/backend.yaml` (JSON-совместимый YAML).
Работающий шлюз отдаёт тот же документ по `/openapi.json`.
Внутренние методы: `contracts/proto/v1/backend.proto`.
Схемы сообщений и будущих результатов ML: `contracts/events/backend.schema.json`.

Поля компании называются company_id. Перед подключением ML, использующего client_id
из БТ, необходимо согласовать преобразование на границе. Внутренние JSON-ответы
gRPC валидируются теми же схемами; формат не меняется молча.

Основные методы описаны в OpenAPI: auth/login/logout/me, companies/current/profile,
news, news/submissions, events, analytics/summary. Списки ограничены limit 1–100 и
offset >= 0. POST ручной загрузки возвращает 202 и объект задания; финальный статус
запрашивается по его ID. Невалидный ввод — 400, отсутствующая сессия — 401,
запрещённая компания — 403, недоступная запись — 404, конфликт ключа — 409.
Временный сбой зависимости — 503, превышение времени gRPC — 504.

Регистрация сотрудника:

| Метод | Назначение |
| --- | --- |
| POST /auth/company-check | Проверка ИНН и названия по мок-справочнику; без создания компании |
| POST /auth/register | Создание заявки; Idempotency-Key обязателен; 201 при создании, 200 при повторе |
| POST /auth/verify-email | Подтверждение одноразовым token; состояние pending_approval |
| POST /auth/resend-verification | Перевыпуск мок-письма; одинаковый ответ 202 |

Все пути выше имеют префикс /api/v1. Ввод регистрации содержит только full_name,
job_title, email, password, company_name, inn; пример — examples/registration.json.
Поля выручки, региона, отрасли и любые неизвестные поля отклоняются.
Пароль 12–72 байта UTF-8. ИНН строковый, для юридических лиц, 10 цифр с контрольной
цифрой. Проверка реквизитов даёт matched_mock с явным provider и checked_at.
422 означает company_name_mismatch либо company_not_found; отсутствие компании
в моках не означает отсутствие в настоящем реестре. Idempotency-Key действует
в рамках email; другой payload с прежним ключом даёт 409 idempotency_conflict.

Регистрация возвращает user, company_check и email_delivery=mock; токен почты
в ответ не включён. User дополнен full_name, job_title, email_verified и access_status.
Новый пользователь получает pending_email, после почты — pending_approval,
после команды оператора — active, при отзыве — blocked. До active доступны
auth/me и logout после входа, бизнес-методы дают 403 membership_not_active.
Самостоятельно изменить компанию, должность для получения прав или подтвердить
привязку через публичный API нельзя. blocked не может войти, старые сессии удаляются.
В production публичные методы мок-регистрации возвращают 503 registration_unavailable.
Для нового юрлица профиль метрик ещё отсутствует: /companies/current/profile — 404
до его отдельной загрузки. В компании okved=null до обогащения, region/sector —
пустые строки, revenue_minor отсутствует. Полный процесс — [регистрация](../architecture/registration.md).

Список news возвращает ArticleSummary с excerpt до 280 символов. Полный текст
получается через news/{id}; он не размножается в каждом ответе списка.

Для приватного объекта чужой компании возвращается 404, чтобы не раскрывать его
существование. Параметр company_id, противоречащий сессии, даёт 403.

Для POST после входа передавайте X-CSRF-Token. Для news/submissions требуется
Idempotency-Key. Тело содержит либо title/text, либо url зарегистрированной
мок-статьи. Пример URL: `https://html.test/articles/grain`.

Неизвестные поля HTTP-ввода отклоняются. В ответы сессии не включается password_hash.
Cookie не используются как внутренняя аутентификация: gRPC требует подписанные
claims с ожидаемой аудиторией. Заголовки компании от внешнего клиента игнорируются.

LLM-контур реализован на бэке с мок-провайдером: обработанные события,
карточки и чат. API и форматы для фронта: [LLM_FRONTEND.md](LLM_FRONTEND.md).
Обмен с ML и сервисная авторизация: [LLM_ML_INTEGRATION.md](LLM_ML_INTEGRATION.md).
Внутренние AI/Insight RPC: `contracts/proto/v1/pipeline.proto`.
Версионируемые схемы: `contracts/events/llm.schema.json`.
Реальные модель, банковский поиск и внешний скоринг пока не подключены.

Оркестратор v2 и безопасное подключение HTTP: [llm-orchestrator.md](../architecture/llm-orchestrator.md).
