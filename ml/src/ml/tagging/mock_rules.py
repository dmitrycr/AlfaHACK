"""Ответы mock-режима для задачи tagging: правила по справочникам (sectors.yaml, aliases.yaml, okved_to_sector.json).
Детерминированы, без сети; нужны для демо, прогрева кеша и тестов. Текст новости читается только как данные:
важность и шум определяются по смыслу правил, а не по значениям, написанным в тексте."""
from __future__ import annotations

import json
import re

from ..refdata import load_refdata
from .aliases import find_companies

_AD = re.compile(r"скидк\w* до \d+|промокод|акци[яи] действует|купите|успей|распродаж|реклам")
_SPORT = re.compile(r"футбол|хоккей|матч|чемпионат|турнир|олимпиад")
_SHOW = re.compile(r"певец|певиц|шоу|актёр|актер|актрис|сериал|кинопремь")
_REGIONS = ["Москва", "Санкт-Петербург", "Татарстан", "Краснодарский край", "Липецкая область", "Свердловская область",
            "Новосибирская область", "Башкортостан", "Красноярский край", "Сахалин"]


def _words(pattern: str, low: str) -> bool:
    return re.search(pattern, low) is not None


def _type(low: str, companies: list[dict]) -> str:
    if _words(r"санкци|пошлин|эмбарго|ограничени\w+ на (?:экспорт|импорт)|таможенн\w+ контрол|запрет", low):
        return "sanctions"
    if _words(r"татарстан|\w+[сц]к\w{1,3} (?:област|кра)|\w+ск\w+ республик|губернатор", low):
        return "regional"
    if _words(r"ключев\w+ ставк|инфляц|курс\w* рубл|рубл\w+ (?:ослаб|укреп)|ввп|безработиц|росстат", low):
        return "macro"
    if _words(r"налог|ндс|ндпи|минфин|минздрав|минстро|законопроект|постановлени|требовани\w+ к|тариф"
              r"|утвердил\w* (?:новые|порядок)|правительств\w+ (?:внесл|утверд|продл)", low):
        return "regulation"
    if _words(r"банк\w* росси|(?<![а-я])цб(?![а-я])", low):
        return "macro"
    if _words(r"нефт|brent|золот|удобрен|пшениц|калийн|прокат|сталь", low) and _words(r"цен|подорож|подешев|котировк|стоимост", low):
        return "commodity"
    if companies:
        return "corporate"
    return "industry"


def _importance(typ: str, low: str, sectors: list[str]) -> int:
    if typ == "macro":
        return 5 if _words(r"ключев\w+ ставк|решени\w+ (?:цб|банка росси)", low) else 4
    if typ == "regulation":
        return 5 if _words(r"правительств\w+", low) and _words(r"налог|ндс|тариф", low) else 4
    if typ in ("sanctions", "commodity"):
        return 4
    if typ in ("corporate", "regional"):
        return 3 if sectors else 2
    return 3


def _direction(low: str) -> str:
    neg = _words(r"снижени\w+ (?:спрос|продаж|добыч|экспорт|выручк)|упал|упали|сократ|ужесточ|ограничен|санкци|ослаб|"
                 r"повышени\w+ (?:налог|ндс|тариф|ставк)|засух|дефицит|подорожани\w+ логист|пошлин", low)
    pos = _words(r"выросл|вырос\b|рост (?:спрос|продаж|выручк|перевозок)|расшир\w+ (?:перечень|список|производств)|"
                 r"запуск|введ\w+ в эксплуатацию|льгот|снижени\w+ ставк|максимум|нарастил|подорожал\w* выше", low)
    return "negative" if neg and not pos else "positive" if pos and not neg else "unclear"


def _horizon(typ: str, low: str) -> str:
    if typ == "commodity" or _words(r"на этой неделе|сегодня|в сентябре|в октябре", low) and typ != "regulation":
        return "short"
    if _words(r"до конца 20\d\d|2027|на несколько лет|программ\w+ модернизации", low):
        return "long"
    return "medium"


def _summary(title: str, lead: str) -> str:
    sentences = re.findall(r"[^.!?]+[.!?]", lead)
    return " ".join(s.strip() for s in sentences[:2]) or title


def tagging_response(messages: list[dict]) -> str:
    prompt = messages[-1]["content"]
    start, end = prompt.rfind("<<<NEWS\n"), prompt.rfind("\nNEWS>>>")
    body = prompt[start + len("<<<NEWS\n"):end] if start >= 0 and end > start else ""
    title, _, lead = body.partition("\n")
    text = f"{title}. {lead}"
    low = text.lower().replace("ё", "е")
    ref = load_refdata()
    from .tagger import load_vocab
    vocab = load_vocab()

    companies = find_companies(text)
    kw = ref.match_keywords(text, "sectors")
    sectors = [s for s, _ in sorted(kw.items(), key=lambda kv: -kv[1])][:2]
    sectors = list(dict.fromkeys(sectors + [c["sector"] for c in companies]))[:3]
    factors = [vocab.factor_ids[f] for f in ref.match_keywords(text, "factors") if f in vocab.factor_ids][:4]
    okved = [c for s in sectors[:2] for c in ref.classes_of_sector(s)[:2]]
    typ = _type(low, companies)

    noise = None
    if _AD.search(low):
        noise = "реклама"
    elif _SPORT.search(low):
        noise = "спорт"
    elif _SHOW.search(low):
        noise = "шоу-бизнес"
    return json.dumps({
        "type": typ, "sectors": sectors, "okved": okved, "companies": [c["name"] for c in companies],
        "regions": [r for r in _REGIONS if r.lower() in text.lower()], "factors": factors,
        "direction": _direction(low), "horizon": _horizon(typ, low), "importance": _importance(typ, low, sectors),
        "summary": _summary(title, lead), "is_noise": noise is not None, "noise_reason": noise,
    }, ensure_ascii=False)
