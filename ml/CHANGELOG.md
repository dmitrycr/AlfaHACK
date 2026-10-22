# CHANGELOG (схемы и контракты)

## 0.1.0
- `schemas.py`: приложение А + `Channel`, `ProfileIn` (раздел 5 ТЗ). Поля приложения А не менялись.
- Локальные типы ML-сервисов (запросы/ответы API, `Chunk`, `RankEvent`) вынесены в `models.py`, чтобы не трогать контракт.

## 0.1.1
- `ProfileIn`/`Channel` не менялись. Добавлен адаптер `company_format.py` (реэкспорт из `ports.py`): `adapt_profile(payload, company=None)` принимает ML-формат или профиль Company Service; `export_profile`, `export_company` выдают `model.Profile` / `model.Company`; `validate_company_profile` зеркалит Go `ValidateProfile`.
- Каналы: русские имена -> `industry|competitor|demand|supply|financial|currency|commodity|regional|regulatory`; `аффилированный` в Company Service нет (пропускается). `source` data/matrix/llm совпадает 1:1. `evidence` строка -> список.
- Новый `scripts/export_profiles.py` (profiles.json, companies.json, companies.csv). Остаются `TODO(align)` в `company_format.py`.

## 0.1.2
- `ProfileIn`: поле `features: Optional[dict]` (`{"buyers": {...}|None, "suppliers": {...}|None}`, см. `profile/features.py`) добавлено на этапе профилей; запись в CHANGELOG дописана задним числом. Остальные поля и `check_insight` не менялись.
