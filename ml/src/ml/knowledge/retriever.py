"""Retriever (интерфейс) и реализация на BM25. Реализация заменяема: эмбеддинги можно подключить позже,
реализовав Retriever.search. Слабый лучший результат (< min_score) даёт пустой список."""
from __future__ import annotations

import json
from abc import ABC, abstractmethod
from dataclasses import asdict, dataclass, field, replace
from datetime import datetime
from pathlib import Path

from rank_bm25 import BM25Okapi

from ..contract import SourceKind, SourceRef
from .tokenizer import tokenize

BANK_URL = "https://bank.local/analytics/"           # TODO(align): реальных адресов у PDF нет, ссылка-заглушка


@dataclass
class Chunk:
    chunk_id: str
    document: str
    page: int
    topic: str
    text: str
    score: float = 0.0
    slug: str = ""                                    # транслитерация названия документа (для url и fragment_id)
    published_at: str = ""                            # ISO-8601; TODO(align): дата из текста/имени файла или дата изменения файла
    date_source: str = ""                             # text | filename | mtime

    @property
    def source_id(self) -> str:
        return f"bank:{self.document}#{self.page}"

    def source_ref(self) -> SourceRef:
        return SourceRef(url=f"{BANK_URL}{self.slug}", name=self.document, published_at=datetime.fromisoformat(self.published_at),
                         kind=SourceKind.bank_analytics, fragment_id=f"{self.slug}#{self.page}")

    def to_result(self) -> dict:
        return {"chunk_id": self.chunk_id, "document": self.document, "page": self.page, "topic": self.topic,
                "score": round(self.score, 4), "text": self.text, "source_id": self.source_id,
                "source_ref": self.source_ref().model_dump(mode="json")}

    def to_json(self) -> dict:
        d = asdict(self)
        d.pop("score")
        return d


class Retriever(ABC):
    @abstractmethod
    def search(self, query: str, k: int = 3, topics: list[str] | None = None) -> list[Chunk]: ...


@dataclass
class BM25Retriever(Retriever):
    chunks: list[Chunk]
    min_score: float = 0.0              # лучший результат слабее порога -> пустой список
    min_coverage: float = 0.0           # доля лемм запроса, которые должны встретиться во фрагменте (отсекает случайные совпадения)
    _bm25: BM25Okapi | None = field(default=None, init=False, repr=False)
    _tokens: list[set] = field(default_factory=list, init=False, repr=False)

    def __post_init__(self):
        docs = [tokenize(c.text) for c in self.chunks]
        if any(docs):
            self._bm25 = BM25Okapi(docs)
            self._tokens = [set(d) for d in docs]

    @classmethod
    def from_file(cls, path: str | Path, min_score: float = 0.0, min_coverage: float = 0.0) -> BM25Retriever:
        p = Path(path)
        chunks = []
        if p.exists():
            for line in p.read_text(encoding="utf-8").splitlines():
                if line.strip():
                    d = json.loads(line)
                    chunks.append(Chunk(**{k: d[k] for k in Chunk.__dataclass_fields__ if k in d}))
        return cls(chunks, min_score, min_coverage)

    def search(self, query: str, k: int = 3, topics: list[str] | None = None) -> list[Chunk]:
        q = tokenize(query)
        if self._bm25 is None or not q:
            return []
        scores = self._bm25.get_scores(q)
        ranked = sorted(((s, i) for i, s in enumerate(scores) if not topics or self.chunks[i].topic in topics),
                        key=lambda t: (-t[0], t[1]))
        if not ranked or ranked[0][0] <= 0 or ranked[0][0] < self.min_score:
            return []
        need = set(q)
        hits = [(s, i) for s, i in ranked if s > 0 and len(need & self._tokens[i]) / len(need) >= self.min_coverage]
        return [replace(self.chunks[i], score=float(s)) for s, i in hits[:k]]
