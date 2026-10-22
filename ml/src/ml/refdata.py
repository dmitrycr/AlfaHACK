"""Справочники: сектора, факторы, алиасы компаний, ОКВЭД, матрица связей, сырьё."""
from __future__ import annotations

import csv
import json
import re
from dataclasses import dataclass, field
from functools import lru_cache
from pathlib import Path

import yaml

from .config import CONFIG_DIR

# Разделы ОКВЭД-2 по классам (первые две цифры)
_SECTIONS = [
    ("A", 1, 3), ("B", 5, 9), ("C", 10, 33), ("D", 35, 35), ("E", 36, 39), ("F", 41, 43),
    ("G", 45, 47), ("H", 49, 53), ("I", 55, 56), ("J", 58, 63), ("K", 64, 66), ("L", 68, 68),
    ("M", 69, 75), ("N", 77, 82), ("O", 84, 84), ("P", 85, 85), ("Q", 86, 88), ("R", 90, 93),
    ("S", 94, 96), ("T", 97, 98), ("U", 99, 99),
]


def section_of_class(cls: str | None) -> str | None:
    try:
        n = int(str(cls)[:2])
    except (TypeError, ValueError):
        return None
    for letter, lo, hi in _SECTIONS:
        if lo <= n <= hi:
            return letter
    return None


def normalize_okved(code: str | None) -> dict:
    """'41.2' и '41.20' -> class '41', group '41.2' (первые 4 знака с точкой), section по таблице."""
    if code is None:
        return {"section": None, "class": None, "group": None}
    s = str(code).strip().replace(",", ".")
    m = re.match(r"^(\d{1,2})(?:\.(\d{1,2})?(\d{0,2}))?", s)
    if not m:
        return {"section": None, "class": None, "group": None}
    cls = m.group(1).zfill(2)
    group = None
    if "." in s:
        digits = s.split(".", 1)[1]
        digits = re.sub(r"\D", "", digits)
        if digits:
            group = f"{cls}.{digits[0]}"
    return {"section": section_of_class(cls), "class": cls, "group": group or cls}


def _wb(alias: str) -> re.Pattern:
    return re.compile(r"(?<![\w])" + re.escape(alias) + r"\w{0,3}(?!\w)", re.IGNORECASE | re.UNICODE)


@dataclass
class RefData:
    sectors: dict[str, list[str]]
    factors: dict[str, list[str]]
    companies: dict[str, dict]
    okved_classes: dict[str, str]
    okved_default: str
    demand: dict[str, dict[str, float]] = field(default_factory=dict)   # from -> to -> strength
    supply: dict[str, dict[str, float]] = field(default_factory=dict)
    commodities: dict[str, dict[str, list[str]]] = field(default_factory=dict)
    _alias_re: list = field(default_factory=list)
    _kw_re: dict = field(default_factory=dict)

    # ---- сектора ----
    @property
    def sector_names(self) -> list[str]:
        return list(self.sectors)

    @property
    def factor_names(self) -> list[str]:
        return list(self.factors)

    def sector_of_class(self, cls: str | None) -> str:
        return self.okved_classes.get(str(cls)[:2], self.okved_default) if cls else self.okved_default

    def classes_of_sector(self, sector: str) -> list[str]:
        return sorted(c for c, s in self.okved_classes.items() if s == sector)

    # ---- компании ----
    def find_companies(self, text: str) -> list[tuple[str, str]]:
        found, seen = [], set()
        for name, sector, pat in self._alias_re:
            if name not in seen and pat.search(text or ""):
                seen.add(name)
                found.append((name, sector))
        return found

    def company_sector(self, name: str) -> str | None:
        n = (name or "").strip().lower()
        for cname, sector, pat in self._alias_re:
            if cname.lower() == n or pat.fullmatch(name.strip()):
                return sector
        return None

    # ---- ключевые слова ----
    def match_keywords(self, text: str, kind: str = "sectors") -> dict[str, int]:
        low = (text or "").lower().replace("ё", "е")
        res: dict[str, int] = {}
        for name, pats in self._kw_re[kind].items():
            n = sum(len(p.findall(low)) for p in pats)
            if n:
                res[name] = n
        return res

    # ---- матрица ----
    def link(self, role: str, client_sector: str, other_sector: str) -> float:
        m = self.demand if role == "demand" else self.supply
        return m.get(client_sector, {}).get(other_sector, 0.0)

    def neighbors(self, sector: str) -> set[str]:
        out = set()
        for m in (self.demand, self.supply):
            out |= set(m.get(sector, {}))
            out |= {s for s, row in m.items() if sector in row}
        return out

    def commodities_of(self, sector: str) -> list[str]:
        d = self.commodities.get(sector, {})
        return sorted(set(d.get("produces", [])) | set(d.get("buys", [])))


def _kw_compile(words: list[str]) -> list[re.Pattern]:
    # Ключ — начало слова слева; несколько слов в ключе допускают любые пробелы
    # «re:...» — готовое регулярное выражение, иначе начало слова
    return [re.compile(w[3:], re.I) if w.startswith("re:") else
            re.compile(r"(?<![а-яa-z0-9])" + re.escape(w.lower().replace("ё", "е")).replace(r"\ ", r"\s+"))
            for w in words]


@lru_cache(maxsize=4)
def load_refdata(config_dir: str | None = None) -> RefData:
    d = Path(config_dir) if config_dir else CONFIG_DIR
    sec = yaml.safe_load((d / "sectors.yaml").read_text(encoding="utf-8"))
    comp = yaml.safe_load((d / "aliases.yaml").read_text(encoding="utf-8"))["companies"]
    ok = json.loads((d / "okved_to_sector.json").read_text(encoding="utf-8"))
    ref = RefData(sectors=sec["sectors"], factors=sec["factors"], companies=comp,
                  okved_classes=ok["classes"], okved_default=ok["default"])
    for name, info in comp.items():
        for alias in sorted(set(info.get("aliases", [name]) + [name]), key=len, reverse=True):
            ref._alias_re.append((name, info["sector"], _wb(alias)))
    ref._kw_re = {
        "sectors": {k: _kw_compile(v) for k, v in sec["sectors"].items()},
        "factors": {k: _kw_compile(v) for k, v in sec["factors"].items()},
    }
    with open(d / "sector_matrix.csv", encoding="utf-8", newline="") as f:
        for r in csv.DictReader(f):
            target = ref.demand if r["role"] == "demand" else ref.supply
            target.setdefault(r["from_sector"], {})[r["to_sector"]] = float(r["strength"])
    with open(d / "sector_commodities.csv", encoding="utf-8", newline="") as f:
        for r in csv.DictReader(f):
            ref.commodities.setdefault(r["sector"], {"produces": [], "buys": []})[r["role"]].append(r["commodity"])
    return ref
