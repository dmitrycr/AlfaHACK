"""FastAPI-обёртка поиска по аналитике банка. Запуск: uvicorn ml.services.knowledge_app:app --port 8102 (из каталога src или с PYTHONPATH=src).
POST /search {query, k?, topics?} -> [{chunk_id, document, page, topic, score, text, source_id, source_ref}]; POST /ingest; GET /health."""
from __future__ import annotations

from dataclasses import asdict
from pathlib import Path

from fastapi import FastAPI, HTTPException
from pydantic import BaseModel, Field

from ..config import CONFIG_DIR, get_config, resolve_path
from ..knowledge import BM25Retriever, Retriever, ingest_dir
from ..knowledge.ingest import load_bank_meta
from ..knowledge.ocr import OcrUnavailable, TesseractOCR

app = FastAPI(title="ML knowledge service")
_retriever: Retriever | None = None


def bank_dir(cfg: dict) -> Path:
    return resolve_path(cfg["paths"]["data_dir"]) / "bank_analytics"


def load_retriever(cfg: dict | None = None) -> BM25Retriever:
    cfg = cfg or get_config()
    k = cfg["knowledge"]
    return BM25Retriever.from_file(resolve_path(cfg["paths"]["bank_chunks"]), float(k["min_score"]), float(k.get("min_coverage", 0.0)),
                                  float(k.get("stale_factor", 1.0)))


def get_retriever() -> Retriever:
    global _retriever
    if _retriever is None:
        _retriever = load_retriever()
    return _retriever


def set_retriever(r: Retriever | None) -> None:
    global _retriever
    _retriever = r


class SearchIn(BaseModel):
    query: str = Field(min_length=1)
    k: int | None = Field(default=None, ge=1, le=20)
    topics: list[str] | None = None


@app.get("/health")
def health() -> dict:
    r = get_retriever()
    chunks = getattr(r, "chunks", [])
    return {"status": "ok", "chunks": len(chunks), "documents": len({c.document for c in chunks}),
            "index": "ready" if chunks else "empty: запустите POST /ingest или scripts/ingest_bank_docs.py"}


@app.post("/search")
def search(body: SearchIn) -> list[dict]:
    k = body.k or int(get_config()["knowledge"]["k"])
    return [c.to_result() for c in get_retriever().search(body.query, k, body.topics)]


@app.post("/ingest")
def ingest() -> dict:
    cfg = get_config()
    k = cfg["knowledge"]
    try:
        rep = ingest_dir(bank_dir(cfg), resolve_path(cfg["paths"]["bank_chunks"]), *k["chunk_chars"], k["overlap"],
                         ocr=TesseractOCR.from_config(cfg), meta=load_bank_meta(CONFIG_DIR / "bank_documents.yaml"))
    except OcrUnavailable as e:
        raise HTTPException(status_code=503, detail=str(e)) from None
    if rep.chunks:
        set_retriever(load_retriever(cfg))
    return asdict(rep) | {"pages": rep.pages}
