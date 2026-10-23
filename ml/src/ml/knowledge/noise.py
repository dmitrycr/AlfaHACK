"""Чистка шума OCR (подписи осей, мусор графиков) перед разбиением страницы на фрагменты. Работает со строками текста;
кеш OCR не меняется, чистка применяется при чтении.

1. Из строк вырезаются «псевдослова»: русские токены от 8 букв, которых нет в словаре pymorphy3, не аббревиатуры и без цифр
   (узязязяяяяя). Чтобы не вырезать настоящие термины, которых нет в словаре (флоутеры, проинфляционных, МосБиржа), токен
   вырезается, только если в нём мало разных букв (менее половины длины). Строка при этом не отбрасывается.
2. Строка остаётся, если в ней есть хотя бы одно из: (а) слово от 3 букв, известное pymorphy3, или латинское слово от 3 букв
   с гласной; (б) аббревиатура из заглавных букв длиной от 2 (ВВП, ИПЦ, ЦБ, USD); (в) месяц с годом или период (Янв.26, Авг.26П,
   1П26, 3К26, 7М26); (г) единицы (г/г, м/м, п.п., б.п., руб., млрд, млн; «%» - только в строке, где есть хотя бы одна буква).
3. Иначе строка отбрасывается, только если в ней преобладают цифры и знаки (больше половины непробельных символов)."""
from __future__ import annotations

import re
from dataclasses import dataclass, field

from .tokenizer import is_known_word

PSEUDO_MIN_LEN = 8
PSEUDO_MAX_DIVERSITY = 0.5            # доля разных букв в токене; у мусора OCR (чачачаччаан...) она мала

_CYR_WORD = re.compile(r"[а-яё]{3,}", re.I)
_LAT_WORD = re.compile(r"(?<![^\W\d_])[A-Za-z]{3,}(?![^\W\d_])")
_ACRONYM = re.compile(r"(?<![^\W\d_])[A-ZА-ЯЁ]{2,}(?![^\W\d_])")
_MONTH_YEAR = re.compile(r"(?<![^\W\d_])(?:янв|фев|мар|апр|май|июн|июл|авг|сен|окт|ноя|дек)\.?\s?\d{2}(?!\d)", re.I)
_PERIOD = re.compile(r"(?<!\d)\d{1,2}[ПпКкМмPpKkHhQqMm]\d{2}(?!\d)")
_UNITS = re.compile(r"г/г|м/м|н/н|к/к|п\.\s?п\.|б\.\s?п\.|руб\.|млрд|млн|трлн|тыс\.", re.I)
_EDGE = re.compile(r"^[\W_]+|[\W_]+$")
_CYR_ONLY = re.compile(r"^[а-яё]+$", re.I)


@dataclass
class Cleaned:
    text: str
    dropped: list[str] = field(default_factory=list)       # отброшенные строки
    removed: list[str] = field(default_factory=list)       # вырезанные псевдослова


def is_pseudo_word(core: str) -> bool:
    if len(core) < PSEUDO_MIN_LEN or not _CYR_ONLY.match(core) or core.isupper() or is_known_word(core):
        return False
    return len(set(core.lower())) / len(core) < PSEUDO_MAX_DIVERSITY


def strip_pseudo_words(line: str) -> tuple[str, list[str]]:
    removed, kept = [], []
    for tok in line.split():
        if is_pseudo_word(_EDGE.sub("", tok)):
            removed.append(tok)
        else:
            kept.append(tok)
    return (" ".join(kept) if removed else line), removed


def keep_reason(line: str) -> str | None:
    """Почему строку нужно оставить (a-г), либо None."""
    if any(is_known_word(m.group()) for m in _CYR_WORD.finditer(line)):
        return "слово"
    if any(re.search(r"[aeiouyAEIOUY]", m.group()) for m in _LAT_WORD.finditer(line)):
        return "латиница"
    if _ACRONYM.search(line):
        return "аббревиатура"
    if _MONTH_YEAR.search(line) or _PERIOD.search(line):
        return "месяц/период"
    if _UNITS.search(line) or ("%" in line and any(ch.isalpha() for ch in line)):
        return "единицы"
    return None


def is_noise_line(line: str) -> bool:
    s = "".join(line.split())
    if not s or keep_reason(line):
        return False
    return sum(ch.isalpha() for ch in s) / len(s) < 0.5          # преобладают цифры и знаки


def clean_ocr_text(text: str) -> Cleaned:
    """Очищенный текст страницы; пустые строки (границы абзацев) сохраняются."""
    out = Cleaned("")
    kept: list[str] = []
    for line in text.split("\n"):
        if not line.strip():
            kept.append(line)
            continue
        new, removed = strip_pseudo_words(line)
        out.removed += removed
        if not new.strip() or is_noise_line(new):
            out.dropped.append(line.strip())
        else:
            kept.append(new)
    out.text = "\n".join(kept)
    return out
