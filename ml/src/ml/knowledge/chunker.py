"""Разбиение текста страницы на фрагменты 500-800 символов с перекрытием 100: по абзацам и предложениям,
слишком длинное предложение режется по словам. Фрагмент не пересекает границу страницы (для ссылки «документ#страница»)."""
from __future__ import annotations

import re

_SENT = re.compile(r"(?<=[.!?…])\s+(?=[А-ЯЁA-Z0-9«\"(])")


def _hard_split(s: str, hi: int) -> list[str]:
    if len(s) <= hi:
        return [s]
    out, cur = [], ""
    for w in s.split():
        if cur and len(cur) + 1 + len(w) > hi:
            out.append(cur)
            cur = w
        else:
            cur = f"{cur} {w}".strip()
    return out + ([cur] if cur else [])


def split_units(text: str, hi: int) -> list[str]:
    units = []
    for para in re.split(r"\n\s*\n", text or ""):
        para = " ".join(para.split())
        for sent in _SENT.split(para):
            if sent.strip():
                units += _hard_split(sent.strip(), hi)
    return units


def _tail(units: list[str], overlap: int) -> list[str]:
    """Хвост предыдущего фрагмента длиной до overlap символов (целые предложения, иначе последние слова)."""
    tail, total = [], 0
    for u in reversed(units):
        if total + len(u) + (1 if tail else 0) > overlap:
            break
        tail.insert(0, u)
        total += len(u) + (1 if len(tail) > 1 else 0)
    if not tail and units:
        words = units[-1].split()
        cut, total = [], 0
        for w in reversed(words):
            if total + len(w) + 1 > overlap:
                break
            cut.insert(0, w)
            total += len(w) + 1
        tail = [" ".join(cut)] if cut else []
    return tail


def chunk_text(text: str, lo: int = 500, hi: int = 800, overlap: int = 100) -> list[str]:
    units = split_units(text, hi)
    chunks: list[tuple[list[str], int]] = []           # (юниты, сколько первых юнитов - перекрытие)
    cur: list[str] = []
    n_overlap = 0
    for u in units:
        if cur and len(" ".join(cur + [u])) > hi:
            chunks.append((cur, n_overlap))
            cur = _tail(cur, overlap)
            n_overlap = len(cur)
            if len(" ".join(cur + [u])) > hi:
                cur, n_overlap = [], 0
        cur.append(u)
    if cur and len(cur) > n_overlap:
        chunks.append((cur, n_overlap))
    if len(chunks) > 1 and len(" ".join(chunks[-1][0])) < lo:      # короткий хвост страницы - присоединяем, если влезает
        merged = chunks[-2][0] + chunks[-1][0][chunks[-1][1]:]
        if len(" ".join(merged)) <= hi:
            chunks[-2:] = [(merged, chunks[-2][1])]
    return [" ".join(c) for c, _ in chunks]
