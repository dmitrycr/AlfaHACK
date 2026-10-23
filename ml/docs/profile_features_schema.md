# Поле `features` профиля

Пример обезличен: идентификатор, веса, доли и показатели концентрации условные, реальных данных в нём нет; структура и ключи настоящие.

Источник: `src/ml/profile/features.py` (`side_features`), вызов в `src/ml/profile/builder.py` (`_side`), выгрузка в `src/ml/company_format.py` (`export_profile`). Пример: `profile_features_example.json` (`buyers` и `suppliers` не null, `concentration` у обеих сторон).

`features` попадает в профиль только при `export.include_features: true` (по умолчанию `false`). Если у профиля `features` нет совсем (старые файлы), ключ не пишется.

| Поле | Тип | Диапазон / правило | Когда null |
|---|---|---|---|
| `features` | object | ключи `buyers`, `suppliers` | ключа нет при `include_features: false` или если `ProfileIn.features` не задан |
| `features.buyers` | object или null | платежи, где клиент - получатель (плательщики = покупатели) | нет платежей стороны (сумма <= 0) после исключения самопереводов и внутригрупповых; либо доля платежей контрагентов из справочника секторов < `profile.concentration_min_coverage` (0.3) |
| `features.suppliers` | object или null | платежи, где клиент - плательщик (получатели = поставщики) | то же условие |
| `<сторона>.sector_shares` | array of object | не более 10 элементов; сортировка по `share` по убыванию, при равенстве по `sector` по алфавиту; сектора с `share` = 0.000 после округления не отдаются | не бывает null |
| `sector_shares[].sector` | string | сектор из справочника контрагентов | - |
| `sector_shares[].share` | number | 3 знака после запятой; сумма платежей контрагентов сектора / сумма платежей контрагентов, найденных в справочнике (неизвестные не считаются) | - |
| `<сторона>.concentration` | object или null | считается от суммы платежей всей стороны, включая контрагентов вне справочника | число контрагентов стороны < `profile.concentration_min_counterparties` (5) |
| `concentration.top1_share` | number | 4 знака; доля крупнейшего контрагента | - |
| `concentration.top5_share` | number | 4 знака; сумма долей до 5 крупнейших контрагентов (если их меньше 5 - всех) | - |
| `concentration.hhi` | number | 4 знака; сумма квадратов долей всех контрагентов | - |

Округление согласовано с бэкендом: `share` - 3 знака, `top1_share`/`top5_share`/`hhi` - 4 знака.

Сырьё и coverage в `features` не входят. Поля `features` нет в `model.Profile` Company Service, OpenAPI и `ReadImport` (см. README, раздел «Поле features профиля»).
