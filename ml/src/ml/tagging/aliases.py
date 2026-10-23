"""Справочник алиасов компаний -> сектор. Формат config/aliases.yaml: `companies: {Имя: {sector, aliases: [...]}}`
или список `- {name, aliases, sector}`. Если файла нет, используется встроенный список крупных компаний.
find_companies(text) ищет алиасы без учёта регистра, с границами слов (допускается падежное окончание до 3 букв)."""
from __future__ import annotations

import logging
import re
from functools import lru_cache
from pathlib import Path

import yaml

from ..config import CONFIG_DIR

log = logging.getLogger("ml.tagging")

# TODO(align): запасной список; список компаний и секторов от команды (players_by_sector) заменит его.
_BUILTIN = {
    "Газпром": ("Нефть и газ", ["Газпром"]), "Роснефть": ("Нефть и газ", ["Роснефть"]),
    "Лукойл": ("Нефть и газ", ["Лукойл"]), "Новатэк": ("Нефть и газ", ["Новатэк"]),
    "Норникель": ("Металлургия и добыча", ["Норникель", "Норильский никель"]),
    "НЛМК": ("Металлургия и добыча", ["НЛМК"]), "Северсталь": ("Металлургия и добыча", ["Северсталь"]),
    "ММК": ("Металлургия и добыча", ["ММК"]), "Русал": ("Металлургия и добыча", ["Русал"]),
    "РусГидро": ("Энергетика", ["РусГидро"]), "Россети": ("Энергетика", ["Россети"]),
    "Интер РАО": ("Энергетика", ["Интер РАО"]),
    "ПИК": ("Строительство", ["ПИК", "ГК ПИК", "ПАО ПИК СЗ"]), "Самолёт": ("Строительство", ["Самолёт", "Самолет"]),
    "ЛСР": ("Строительство", ["ЛСР"]), "Эталон": ("Строительство", ["Эталон"]),
    "Магнит": ("Розничная торговля", ["Магнит"]), "X5": ("Розничная торговля", ["X5", "Пятёрочка", "Перекрёсток"]),
    "Лента": ("Розничная торговля", ["Лента"]), "Wildberries": ("Розничная торговля", ["Wildberries", "Вайлдберриз"]),
    "Ozon": ("Розничная торговля", ["Ozon", "Озон"]),
    "АвтоВАЗ": ("Автомобили", ["АвтоВАЗ", "Lada"]), "КамАЗ": ("Автомобили", ["КамАЗ"]),
    "РЖД": ("Транспорт и логистика", ["РЖД"]), "Аэрофлот": ("Транспорт и логистика", ["Аэрофлот"]),
    "Яндекс": ("ИТ и телеком", ["Яндекс"]), "МТС": ("ИТ и телеком", ["МТС"]),
    "Ростелеком": ("ИТ и телеком", ["Ростелеком"]),
    "Сбербанк": ("Финансы", ["Сбербанк", "Сбер"]), "ВТБ": ("Финансы", ["ВТБ"]),
    "ФосАгро": ("Химия и удобрения", ["ФосАгро"]), "Уралкалий": ("Химия и удобрения", ["Уралкалий"]),
    "Русагро": ("Агро и пищевая промышленность", ["Русагро"]),
    "Ростех": ("Машиностроение и промышленность", ["Ростех"]),
}


def _compile(alias: str) -> re.Pattern:
    suffix = r"[а-яё]{0,3}" if re.search(r"[а-яё]{4,}", alias, re.I) else ""    # «Лукойла», но не «ПИКа» из ПИК
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
