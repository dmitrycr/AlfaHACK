"""Токенизация для BM25: нижний регистр, слова из букв и цифр, лемматизация pymorphy3 (если установлена),
иначе простая обрезка окончаний."""
from __future__ import annotations

import re
from functools import lru_cache

_WORD = re.compile(r"[a-zа-яё0-9]+")
_STOP = frozenset("и в во на по с со к о об а но не что как это для из от за до или то же бы ли у при".split())
_ENDINGS = sorted("""ого его ому ему ами ями ыми ими ах ях ов ев ей ой ий ый ая яя ое ее ую юю ые ие ом ем ою ею
а я у ю ы и е о ь""".split(), key=len, reverse=True)

try:                                                   # pymorphy3 опциональна
    import pymorphy3
    _MORPH = pymorphy3.MorphAnalyzer()
except Exception:                                      # noqa: BLE001 - любая неудача установки/словарей: запасной режим
    _MORPH = None


def stem(word: str) -> str:
    """Запасной вариант без словарей: отбрасываем самое длинное окончание, оставляя основу из 3+ букв."""
    if not re.search(r"[а-яё]", word):
        return word
    for end in _ENDINGS:
        if word.endswith(end) and len(word) - len(end) >= 3:
            return word[:-len(end)]
    return word


@lru_cache(maxsize=50_000)
def _lemma(word: str, use_morph: bool) -> str:
    if use_morph and _MORPH is not None and re.search(r"[а-яё]", word):
        return _MORPH.parse(word)[0].normal_form.replace("ё", "е")
    return stem(word.replace("ё", "е"))


def tokenize(text: str, use_morph: bool | None = None) -> list[str]:
    """use_morph=None: лемматизировать, если pymorphy3 доступна; False: принудительно запасной режим."""
    morph = (_MORPH is not None) if use_morph is None else use_morph
    return [_lemma(w, morph) for w in _WORD.findall((text or "").lower()) if w not in _STOP]
