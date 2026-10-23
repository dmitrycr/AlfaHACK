"""Защита от промпт-инъекций: текст новости - данные. sanitize() чистит HTML, пробелы, управляющие символы,
вырезает подозрительные конструкции и обрезает текст. Не падает на пустом тексте и тексте без букв."""
from __future__ import annotations

import html
import re
from typing import NamedTuple

REMOVED = "[удалено]"

_DELIM = re.compile(r"<<<\s*NEWS|NEWS\s*>>>", re.I)          # нельзя закрыть блок данных изнутри текста
_TAG = re.compile(r"</?[a-zA-Z!][^>]*>")
_CTRL = re.compile(r"[\x00-\x08\x0b-\x1f\x7f​-‏ -‮⁠﻿]")
_WS = re.compile(r"\s+")
_SUSPICIOUS = [re.compile(p, re.I) for p in (
    r"игнорируй\w*(?:\s+(?:все|всё|любые))?(?:\s+(?:предыдущ|прошл|прежн|выше)\w*)?(?:\s+(?:инструкци|команд|указани)\w*)?",
    r"забудь\w*\s+(?:все\s+)?(?:предыдущ|прошл|прежн)\w*\s+(?:инструкци|команд|указани)\w*",
    r"ты\s+теперь\b",
    r"system\s*prompt",
    r"системн\w+\s+(?:промпт|сообщени|инструкци)\w*",
    r"раскро\w*\s+(?:свой\s+|мой\s+|секретн\w+\s+)?(?:api[\s-]*)?ключ\w*",
    r"ignore\s+(?:all\s+|any\s+)?(?:previous|prior|above)\s+(?:instructions|prompts?)",
    r"disregard\s+(?:all\s+)?(?:previous|prior|above)\s+\w+",
    r"you\s+are\s+now\b",
    r"верни\w*\s+(?:только\s+)?(?:json\s*)?\{[^{}]*\}",
    r"(?:выведи|покажи|напиши)\w*\s+(?:свой\s+)?(?:промпт|инструкци\w+)",
)]


class Sanitized(NamedTuple):
    text: str
    suspicious: bool


def sanitize(text: object, max_chars: int = 800) -> Sanitized:
    """-> (очищенный текст не длиннее max_chars, были ли подозрительные конструкции)."""
    t = "" if text is None else str(text)
    suspicious = bool(_DELIM.search(t))
    t = _DELIM.sub(" ", t)
    t = _TAG.sub(" ", t)
    t = _TAG.sub(" ", html.unescape(t))
    t = _CTRL.sub(" ", t)
    for pat in _SUSPICIOUS:
        t, n = pat.subn(REMOVED, t)
        suspicious = suspicious or n > 0
    t = _WS.sub(" ", t).strip()
    return Sanitized(t[:max_chars].rstrip(), suspicious)
