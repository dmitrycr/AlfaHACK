"""Тегирование событий: один вызов LLM на событие (кеш по event_id), результат - Event контракта v0.1 (contract.py).
Новость - данные, а не инструкции: текст проходит guard.sanitize, значения из ответа модели ограничены списками."""
from __future__ import annotations

import hashlib
import logging
import re
from collections import Counter
from dataclasses import dataclass
from datetime import datetime
from functools import lru_cache
from pathlib import Path
from typing import Callable, Literal, Optional
from urllib.parse import urlparse

import yaml
from pydantic import BaseModel, Field, HttpUrl, TypeAdapter, ValidationError

from ..config import CONFIG_DIR, get_config
from ..contract import Event, SourceKind, SourceRef
from ..llm import LLMFormatError, MockLLM, build_llm
from ..llm.base import LLMClient
from ..refdata import RefData, load_refdata
from .aliases import find_companies
from .guard import sanitize

log = logging.getLogger("ml.tagging")

PROMPT_VERSION = "tagging-v3.1"
PROMPT_PATH = Path(__file__).resolve().parents[1] / "prompts" / f"{PROMPT_VERSION}.txt"
TITLE_MAX = 300
_PLACEHOLDER = re.compile(r"\{(sectors|factors|companies_hint|title|lead)\}")
_CYRILLIC = re.compile(r"[а-яё]", re.I)
_TERMINAL = re.compile(r"[.!?…»\")]$")
_OKVED = re.compile(r"^\d{2}$")
_URL = TypeAdapter(HttpUrl)
_DT = TypeAdapter(datetime)


class LLMTags(BaseModel):
    """Ответ модели (JSON). Списки значений проверяются после разбора: чужие значения отбрасываются с записью в лог."""
    type: Literal["regulation", "macro", "commodity", "corporate", "industry", "regional", "sanctions"]
    sectors: list[str] = Field(default_factory=list)
    okved: list[str] = Field(default_factory=list)
    companies: list[str] = Field(default_factory=list)
    regions: list[str] = Field(default_factory=list)
    factors: list[str] = Field(default_factory=list)
    direction: Literal["positive", "negative", "unclear"]
    horizon: Literal["short", "medium", "long"]
    importance: int = Field(ge=1, le=5)
    summary: str
    is_noise: bool = False
    noise_reason: Optional[str] = None


class TagInput(BaseModel):
    """Вход Event Service: {event_id, title, text, published_at, source, url}; лишние поля игнорируются."""
    event_id: str
    title: str = ""
    text: str = ""
    published_at: Optional[str] = None
    source: str = ""
    url: Optional[str] = None


class TagResult(BaseModel):
    """Ответ /tag. Поля шума живут вне Event."""
    event: Optional[Event] = None
    is_noise: bool = False
    noise_reason: Optional[str] = None
    suspicious: bool = False


@dataclass(frozen=True)
class Vocab:
    sectors: tuple[str, ...]          # только enabled-сектора: они идут в промпт и в Event.sectors
    factors: tuple[str, ...]          # идентификаторы контракта (key_rate, usd, ...)
    factor_ids: dict[str, str]        # русское имя из sectors.yaml -> идентификатор
    ref: RefData | None = None        # справочник секторов: приведение названий к каноничным, enabled


@lru_cache(maxsize=4)
def load_vocab(config_dir: str | None = None) -> Vocab:
    d = Path(config_dir) if config_dir else CONFIG_DIR
    ref = load_refdata(config_dir)
    ids = yaml.safe_load((d / "factor_ids.yaml").read_text(encoding="utf-8"))
    return Vocab(tuple(ref.enabled_sector_names), tuple(ids.values()), dict(ids), ref)      # выключенные сектора (enabled: false) не входят


def build_tagging_llm(cfg: dict | None = None) -> LLMClient:
    """build_llm + осмысленные ответы mock-режима для задачи tagging (правила по справочникам, без сети)."""
    client = build_llm(cfg or get_config())
    if isinstance(client, MockLLM):
        from .mock_rules import tagging_response
        client.responders.setdefault("tagging", tagging_response)
    return client


def _canon_sectors(values: list[str], vocab: Vocab, eid: str, store=None, stats: Counter | None = None) -> list[str]:
    """Названия секторов из ответа модели -> каноничные (через aliases в sectors.yaml). Несопоставленные и выключенные
    (enabled: false) отбрасываются; несопоставленные пишутся в лог и в журнал LLM с event_id."""
    out, unmatched = [], []
    stats = stats if stats is not None else Counter()
    for v in values:
        v = str(v).strip()
        canon = vocab.ref.canonical_sector(v) if vocab.ref else (v if v in vocab.sectors else None)
        if canon is None:
            if v:
                unmatched.append(v)
                stats["unmatched"] += 1
        elif canon in vocab.sectors:
            stats["exact" if canon == v else "alias"] += 1
            if canon not in out:
                out.append(canon)
            if canon != v:
                log.info("event %s: сектор %r приведён к %r", eid, v, canon)
        else:
            stats["disabled"] += 1
            log.info("event %s: сектор %r выключен (enabled: false), отброшен", eid, canon)
    if unmatched:
        log.warning("event %s: сектора %s не сопоставлены с каноничными, отброшены", eid, unmatched)
        if store is not None:
            store.log_call(run_id=f"tag:{eid}", task="tagging", model=None, prompt_version=PROMPT_VERSION, status="note",
                           error=f"event {eid}: сектора не сопоставлены с каноничными: {unmatched}")
    return out


def _keep(values: list[str], allowed, what: str, eid: str) -> list[str]:
    out, dropped = [], []
    for v in values:
        v = str(v).strip()
        if v in allowed:
            if v not in out:
                out.append(v)
        elif v:
            dropped.append(v)
    if dropped:
        log.warning("event %s: значения %s вне списка отброшены: %s", eid, what, dropped)
    return out


class Tagger:
    NS = f"tags:{PROMPT_VERSION}"          # к нему добавляется хеш набора секторов в промпте (см. __init__)

    def __init__(self, llm: LLMClient | None = None, cfg: dict | None = None, vocab: Vocab | None = None,
                 now: Callable[[], datetime] | None = None):
        self.cfg = cfg or get_config()
        self.llm = llm or build_tagging_llm(self.cfg)
        t = self.cfg.get("tagging", {})
        self.max_chars = int(t.get("max_chars", 800))          # TODO(align): в config.yaml есть lead_chars=700, ТЗ требует 800
        self.min_chars = int(t.get("min_text_chars", 20))
        self.vocab = vocab or load_vocab()
        self.NS = f"{Tagger.NS}:{hashlib.sha1(chr(10).join(sorted(self.vocab.sectors)).encode()).hexdigest()[:8]}"   # другой набор секторов = другой промпт
        self.sector_stats: Counter = Counter()     # exact / alias (приведено через алиас) / unmatched / disabled
        self.template = PROMPT_PATH.read_text(encoding="utf-8")
        self._now = now or (lambda: datetime.now().astimezone())

    # ---- публичный API ----
    def tag_event(self, event: dict | TagInput) -> TagResult:
        ev = event if isinstance(event, TagInput) else TagInput.model_validate(event)
        eid = ev.event_id.strip()
        cached = self.llm.store.kv_get(self.NS, eid)
        if cached is not None:
            return TagResult.model_validate_json(cached)                      # F1-04: повторно LLM не вызываем

        title, s1 = sanitize(ev.title, TITLE_MAX)
        text, s2 = sanitize(ev.text, self.max_chars)
        suspicious = s1 or s2
        if suspicious:
            log.warning("event %s: в тексте найдены подозрительные конструкции, удалены", eid)

        source, reason = self._source(ev)
        if source is None:
            log.warning("event %s: Event не строится: %s", eid, reason)
            return TagResult(noise_reason=reason, suspicious=suspicious)

        local_noise = self._local_noise(title, text)
        if not title and not text:
            return TagResult(is_noise=True, noise_reason=local_noise, suspicious=suspicious)

        found = find_companies(f"{title} {text}")
        messages = [{"role": "user", "content": self._prompt(title, text, found)}]
        try:
            tags: LLMTags = self.llm.complete("tagging", messages, LLMTags, run_id=f"tag:{eid}",
                                              prompt_version=PROMPT_VERSION)
        except LLMFormatError as e:
            log.error("event %s: ответ модели не прошёл проверку: %s", eid, e)
            return TagResult(noise_reason="ошибка разбора ответа модели", suspicious=suspicious)

        result = self._build(eid, title, source, tags, found, local_noise, suspicious)
        if result.event is not None:
            self.llm.store.kv_put(self.NS, eid, result.model_dump_json())
        return result

    def tag_batch(self, events: list[dict]) -> list[TagResult]:
        return [self.tag_event(e) for e in events]

    # ---- внутреннее ----
    def _source(self, ev: TagInput) -> tuple[SourceRef | None, str | None]:
        try:
            url = _URL.validate_python((ev.url or "").strip())
        except ValidationError:
            return None, "нет источника"
        try:
            published = _DT.validate_python((ev.published_at or "").strip())
        except ValidationError:
            return None, "нет даты публикации"
        name = (ev.source or "").strip() or (urlparse(str(url)).hostname or "")
        return SourceRef(url=url, name=name, published_at=published, kind=SourceKind.news), None

    def _local_noise(self, title: str, text: str) -> str | None:
        if not title and not text:
            return "пустой текст"
        if len(text) < self.min_chars or (len(text) < 80 and not _TERMINAL.search(text)):
            return "пустой или обрезанный текст"
        if not _CYRILLIC.search(f"{title} {text}"):
            return "нерусский язык"
        return None

    def _prompt(self, title: str, lead: str, found: list[dict]) -> str:
        hint = ", ".join(f"{c['name']} ({c['sector']})" for c in found) or "нет"
        vals = {"sectors": ", ".join(self.vocab.sectors), "factors": ", ".join(self.vocab.factors),
                "companies_hint": hint, "title": title, "lead": lead}
        return _PLACEHOLDER.sub(lambda m: vals[m.group(1)], self.template)       # один проход: текст не подставляется повторно

    def _build(self, eid: str, title: str, source: SourceRef, tags: LLMTags, found: list[dict],
               local_noise: str | None, suspicious: bool) -> TagResult:
        sectors = _canon_sectors(tags.sectors, self.vocab, eid, self.llm.store, self.sector_stats)
        sectors += [s for s in _canon_sectors([c["sector"] for c in found], self.vocab, eid) if s not in sectors]
        factors = _keep(tags.factors, self.vocab.factors, "factors", eid)
        okved = _keep(tags.okved, {v for v in map(str.strip, tags.okved) if _OKVED.match(v)}, "okved", eid)
        companies = list(dict.fromkeys([c["name"] for c in found] + [c.strip() for c in tags.companies if c.strip()]))
        regions = list(dict.fromkeys(r.strip() for r in tags.regions if r.strip()))
        is_noise = tags.is_noise or local_noise is not None
        noise_reason = (local_noise or tags.noise_reason or "не указана") if is_noise else None
        if is_noise:
            log.info("event %s: шум: %s", eid, noise_reason)
        try:
            event = Event(event_id=eid, title=title, summary=tags.summary.strip() or title, sources=[source],
                          type=tags.type, sectors=sectors, okved=okved, companies=companies, regions=regions,
                          factors=factors, direction=tags.direction, horizon=tags.horizon,
                          importance=1 if is_noise else tags.importance, collected_at=self._now())
        except ValidationError as e:
            log.error("event %s: не удалось собрать Event: %s", eid, e)
            return TagResult(noise_reason="ошибка сборки Event", suspicious=suspicious)
        return TagResult(event=event, is_noise=is_noise, noise_reason=noise_reason, suspicious=suspicious)
