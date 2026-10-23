"""Порты интеграции с чужими сервисами: ProfileProvider, EventProvider, NewsProvider, BankSearch.
Каждый порт имеет HTTP-клиент (адрес из переменной окружения *_SERVICE_URL, формат TODO(align)) и фикстурную заглушку.
Если переменная не задана, используется заглушка. Несовпадение полей чужого сервиса лечится адаптерами здесь,
а не правкой ML-кода."""
from __future__ import annotations

import json
import os
from abc import ABC, abstractmethod
from dataclasses import dataclass
from pathlib import Path

import httpx
from sklearn.feature_extraction.text import TfidfVectorizer

from .company_format import adapt_profile, export_company, export_profile, validate_company_profile  # noqa: F401
from .config import resolve_path
from .knowledge.retriever import BM25Retriever, Retriever
from .knowledge.tokenizer import tokenize
from .models import Chunk, NewsIn, RankEvent, StoredEvent
from .schemas import ProfileIn


class NotFound(Exception):
    pass


# ---------------------------------------------------------------- профили
class ProfileProvider(ABC):
    @abstractmethod
    def get_profile(self, client_id: str) -> ProfileIn: ...

    @abstractmethod
    def list_clients(self) -> list[str]: ...


# Адаптер формата Company Service живёт в company_format.py (без зависимостей от других этапов).


class FileProfileProvider(ProfileProvider):
    """Профили из profiles.json (результат build_profiles.py) или из фикстуры profiles_demo.json."""

    def __init__(self, path: str | Path):
        raw = json.loads(Path(path).read_text(encoding="utf-8"))
        profiles = raw.get("profiles", raw)
        self._p = {cid: adapt_profile(p) for cid, p in profiles.items() if not cid.startswith("_")}

    def get_profile(self, client_id: str) -> ProfileIn:
        if client_id not in self._p:
            raise NotFound(f"профиль {client_id} не найден")
        return self._p[client_id]

    def list_clients(self) -> list[str]:
        return sorted(self._p)


class HttpProfileProvider(ProfileProvider):
    def __init__(self, base_url: str, timeout: float = 10):
        self.base, self.timeout = base_url.rstrip("/"), timeout

    def get_profile(self, client_id: str) -> ProfileIn:
        # TODO(align): путь; Company Service отдаёт Profile и Company раздельно (gateway /companies/current/profile
        # требует сессию), для формата CS нужен второй запрос за документом компании -> adapt_profile(p, company)
        r = httpx.get(f"{self.base}/profile/{client_id}", timeout=self.timeout)
        if r.status_code == 404:
            raise NotFound(client_id)
        r.raise_for_status()
        return adapt_profile(r.json())

    def list_clients(self) -> list[str]:
        r = httpx.get(f"{self.base}/clients", timeout=self.timeout)  # TODO(align)
        r.raise_for_status()
        return list(r.json())


# ---------------------------------------------------------------- события
class EventProvider(ABC):
    @abstractmethod
    def get_event(self, event_id: str) -> StoredEvent: ...

    @abstractmethod
    def list_events(self) -> list[StoredEvent]: ...


class FixtureEventProvider(EventProvider):
    """Новости из JSON-файла; теги считает Tagger (один раз на событие, кеш)."""

    def __init__(self, news_path: str | Path, tagger):
        raw = json.loads(Path(news_path).read_text(encoding="utf-8"))
        self._raw = {n["event_id"]: n for n in raw}
        self.tagger = tagger
        self._cache: dict[str, StoredEvent] = {}

    def add(self, news: NewsIn | dict) -> StoredEvent:
        n = news if isinstance(news, NewsIn) else NewsIn.model_validate(news)
        tags = self.tagger.tag(n)
        ev = StoredEvent(tags=tags, text=n.text, url=n.url, source=n.source)
        self._cache[n.event_id] = ev
        return ev

    def get_event(self, event_id: str) -> StoredEvent:
        if event_id in self._cache:
            return self._cache[event_id]
        if event_id not in self._raw:
            raise NotFound(f"событие {event_id} не найдено")
        return self.add(self._raw[event_id])

    def list_events(self) -> list[StoredEvent]:
        return [self.get_event(e) for e in self._raw]


class HttpEventProvider(EventProvider):
    def __init__(self, base_url: str, timeout: float = 10):
        self.base, self.timeout = base_url.rstrip("/"), timeout

    @staticmethod
    def _adapt(d: dict) -> StoredEvent:
        # TODO(align): формат Event Service
        return StoredEvent(tags=RankEvent.model_validate(d.get("tags", d)), text=d.get("text", ""),
                           url=d.get("url"), source=d.get("source"))

    def get_event(self, event_id: str) -> StoredEvent:
        r = httpx.get(f"{self.base}/events/{event_id}", timeout=self.timeout)
        if r.status_code == 404:
            raise NotFound(event_id)
        r.raise_for_status()
        return self._adapt(r.json())

    def list_events(self) -> list[StoredEvent]:
        r = httpx.get(f"{self.base}/events", timeout=self.timeout)
        r.raise_for_status()
        return [self._adapt(d) for d in r.json()]


# ---------------------------------------------------------------- поиск похожих новостей
class NewsProvider(ABC):
    @abstractmethod
    def search_news(self, query: str, k: int = 3, exclude_id: str | None = None) -> list[StoredEvent]: ...


class FixtureNewsProvider(NewsProvider):
    """TF-IDF по заголовкам и текстам событий (связанные события, P1)."""

    def __init__(self, events: EventProvider):
        self.events = events
        self._vec = self._mat = self._items = None

    def _build(self):
        items = [e for e in self.events.list_events() if not e.tags.is_noise]
        self._items = items
        if items:
            self._vec = TfidfVectorizer(tokenizer=tokenize, lowercase=False, token_pattern=None)
            self._mat = self._vec.fit_transform([f"{e.tags.title} {e.text}" for e in items])

    def search_news(self, query: str, k: int = 3, exclude_id: str | None = None) -> list[StoredEvent]:
        if self._items is None:
            self._build()
        if not self._items:
            return []
        sims = (self._mat @ self._vec.transform([query]).T).toarray().ravel()
        order = sorted(range(len(self._items)), key=lambda i: (-sims[i], self._items[i].tags.event_id))
        return [self._items[i] for i in order if sims[i] > 0.05 and self._items[i].tags.event_id != exclude_id][:k]


class HttpNewsProvider(NewsProvider):
    def __init__(self, base_url: str, timeout: float = 10):
        self.base, self.timeout = base_url.rstrip("/"), timeout

    def search_news(self, query: str, k: int = 3, exclude_id: str | None = None) -> list[StoredEvent]:
        r = httpx.get(f"{self.base}/search", params={"q": query, "k": k}, timeout=self.timeout)  # TODO(align)
        r.raise_for_status()
        return [HttpEventProvider._adapt(d) for d in r.json() if d.get("event_id") != exclude_id][:k]


# ---------------------------------------------------------------- аналитика банка
class BankSearch(ABC):
    @abstractmethod
    def search(self, query: str, k: int = 3, topics: list[str] | None = None) -> list[Chunk]: ...


class LocalBankSearch(BankSearch):
    def __init__(self, retriever: Retriever):
        self.retriever = retriever

    def search(self, query, k=3, topics=None):
        return self.retriever.search(query, k, topics)


class HttpBankSearch(BankSearch):
    def __init__(self, base_url: str, timeout: float = 10):
        self.base, self.timeout = base_url.rstrip("/"), timeout

    def search(self, query, k=3, topics=None):
        r = httpx.post(f"{self.base}/search", json={"query": query, "k": k, "topics": topics}, timeout=self.timeout)
        r.raise_for_status()
        return [Chunk(chunk_id=h["chunk_id"], document=h["document"], page=h["page"], topic=h["topic"],
                      text=h["text"], score=h["score"]) for h in r.json()]


# ---------------------------------------------------------------- сборка
@dataclass
class Ports:
    profiles: ProfileProvider
    events: EventProvider
    news: NewsProvider
    bank: BankSearch


def build_retriever(cfg: dict) -> BM25Retriever:
    chunks = resolve_path(cfg["paths"]["bank_chunks"])
    if not chunks.exists():                                   # без ingest: демо-фрагменты из фикстур
        from .knowledge.ingest import ingest_dir
        fx = resolve_path(cfg["paths"]["fixtures"]) / "bank_analytics"
        chunks.parent.mkdir(parents=True, exist_ok=True)
        lo, hi = cfg["knowledge"]["chunk_chars"]
        ingest_dir(fx, chunks, lo, hi, cfg["knowledge"]["overlap"])
    return BM25Retriever.from_file(chunks, cfg["knowledge"]["min_score"])


def build_ports(cfg: dict, tagger) -> Ports:
    fx = resolve_path(cfg["paths"]["fixtures"])
    prof_url, ev_url = os.environ.get("PROFILE_SERVICE_URL"), os.environ.get("EVENT_SERVICE_URL")
    news_url, kn_url = os.environ.get("NEWS_SERVICE_URL"), os.environ.get("KNOWLEDGE_SERVICE_URL")
    if prof_url:
        profiles: ProfileProvider = HttpProfileProvider(prof_url)
    else:
        real = resolve_path(cfg["paths"]["profiles"])
        profiles = FileProfileProvider(real if real.exists() else fx / "profiles_demo.json")
    events: EventProvider = HttpEventProvider(ev_url) if ev_url else FixtureEventProvider(fx / "news_30.json", tagger)
    news: NewsProvider = HttpNewsProvider(news_url) if news_url else FixtureNewsProvider(events)
    bank: BankSearch = HttpBankSearch(kn_url) if kn_url else LocalBankSearch(build_retriever(cfg))
    return Ports(profiles, events, news, bank)
