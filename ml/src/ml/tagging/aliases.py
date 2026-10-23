"""Справочник алиасов компаний -> сектор. Формат config/aliases.yaml: `companies: {Имя: {sector, aliases: [...]}}`
или список `- {name, aliases, sector}`. Если файла нет, используется встроенный список крупных компаний.
find_companies(text) ищет алиасы с границами слов: длинные (от 5 символов) без учёта регистра и с падежным окончанием до 3 букв,
короткие (до 4 символов) только в точном написании из справочника."""
from __future__ import annotations

import logging
import re
from functools import lru_cache
from pathlib import Path

import yaml

from ..config import CONFIG_DIR

log = logging.getLogger("ml.tagging")

# Запасной список (сектора - канонические названия players_by_sector); основной источник - config/aliases.yaml из xlsx.
_BUILTIN = {
    "Газпром": ("Нефть и газ", ["Газпром"]), "Роснефть": ("Нефть и газ", ["Роснефть"]),
    "Лукойл": ("Нефть и газ", ["Лукойл"]), "Новатэк": ("Нефть и газ", ["Новатэк"]),
    "Норникель": ("Металлургия", ["Норникель", "Норильский никель"]),
    "НЛМК": ("Металлургия", ["НЛМК"]), "Северсталь": ("Металлургия", ["Северсталь"]),
    "ММК": ("Металлургия", ["ММК"]), "Русал": ("Металлургия", ["Русал"]),
    "РусГидро": ("Энергетика и коммунальные услуги", ["РусГидро"]), "Россети": ("Энергетика и коммунальные услуги", ["Россети"]),
    "Интер РАО": ("Энергетика и коммунальные услуги", ["Интер РАО"]),
    "ПИК": ("Строительство и девелопмент", ["ПИК", "ГК ПИК", "ПАО ПИК СЗ"]), "Самолёт": ("Строительство и девелопмент", ["Самолёт", "Самолет"]),
    "ЛСР": ("Строительство и девелопмент", ["ЛСР"]), "Эталон": ("Строительство и девелопмент", ["Эталон"]),
    "Магнит": ("Розничная торговля и e-commerce", ["Магнит"]), "X5": ("Розничная торговля и e-commerce", ["X5", "Пятёрочка", "Перекрёсток"]),
    "Лента": ("Розничная торговля и e-commerce", ["Лента"]), "Wildberries": ("Розничная торговля и e-commerce", ["Wildberries", "Вайлдберриз"]),
    "Ozon": ("Розничная торговля и e-commerce", ["Ozon", "Озон"]),
    "АвтоВАЗ": ("Машиностроение, автопром, ОПК", ["АвтоВАЗ", "Lada"]), "КамАЗ": ("Машиностроение, автопром, ОПК", ["КамАЗ"]),
    "РЖД": ("Транспорт и логистика", ["РЖД"]), "Аэрофлот": ("Транспорт и логистика", ["Аэрофлот"]),
    "Яндекс": ("IT и телеком", ["Яндекс"]), "МТС": ("IT и телеком", ["МТС"]),
    "Ростелеком": ("IT и телеком", ["Ростелеком"]),
    "Сбербанк": ("Банки, страхование, финансы", ["Сбербанк", "Сбер"]), "ВТБ": ("Банки, страхование, финансы", ["ВТБ"]),
    "ФосАгро": ("Химия, удобрения, нефтехимия", ["ФосАгро"]), "Уралкалий": ("Химия, удобрения, нефтехимия", ["Уралкалий"]),
    "Русагро": ("Пищевая промышленность и агро", ["Русагро"]),
    "Ростех": ("Машиностроение, автопром, ОПК", ["Ростех"]),
}


def _compile(alias: str) -> re.Pattern:
    if len(alias) <= 4:                      # короткие алиасы (ПИК, МТС, Сбер): только точное написание, «пик» - не компания
        return re.compile(r"(?<!\w)" + re.escape(alias) + r"(?!\w)")
    suffix = r"[а-яё]{0,3}" if re.search(r"[а-яё]{4,}", alias, re.I) else ""    # «Лукойла»: падежное окончание
    return re.compile(r"(?<!\w)" + re.escape(alias) + suffix + r"(?!\w)", re.I | re.U)


def _entries(path: Path) -> list[tuple[str, str, list[str]]]:
    if path.exists():
        data = yaml.safe_load(path.read_text(encoding="utf-8")) or {}
        raw = data.get("companies", data) if isinstance(data, dict) else data
        if isinstance(raw, dict):
            return [(n, i["sector"], list(i.get("aliases", []))) for n, i in raw.items()]
        if isinstance(raw, list):
            return [(i["name"], i["sector"], list(i.get("aliases", []))) for i in raw]
    else:
        log.warning("нет %s, используется встроенный список компаний", path)
    return [(n, s, a) for n, (s, a) in _BUILTIN.items()]


@lru_cache(maxsize=4)
def _load(path: str) -> list[tuple[str, str, re.Pattern]]:
    out = []
    for name, sector, aliases in _entries(Path(path)):
        for alias in sorted(set(aliases) | {name}, key=len, reverse=True):
            out.append((name, sector, _compile(alias)))
    return out


def find_companies(text: str, path: str | Path | None = None) -> list[dict]:
    """-> [{name, sector}] в порядке появления в справочнике, без повторов."""
    found, seen = [], set()
    for name, sector, pat in _load(str(path or CONFIG_DIR / "aliases.yaml")):
        if name not in seen and pat.search(text or ""):
            seen.add(name)
            found.append({"name": name, "sector": sector})
    return found
