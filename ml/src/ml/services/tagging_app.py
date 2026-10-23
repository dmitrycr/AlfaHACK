"""FastAPI-обёртка тегирования. Запуск: uvicorn ml.services.tagging_app:app --port 8101 (из каталога src или с PYTHONPATH=src).
POST /tag {event_id,title,text,published_at,source,url} -> {event, is_noise, noise_reason, suspicious}; POST /tag/batch (массив); GET /health."""
from __future__ import annotations

import logging

from fastapi import FastAPI, HTTPException

from ..config import get_config
from ..llm import BudgetExceeded, LLMError, LLMNetworkError
from ..tagging import PROMPT_VERSION, Tagger, TagInput, TagResult

log = logging.getLogger("ml.tagging")
app = FastAPI(title="ML tagging service")
_tagger: Tagger | None = None


def get_tagger() -> Tagger:
    global _tagger
    if _tagger is None:
        _tagger = Tagger()
    return _tagger


def set_tagger(tagger: Tagger | None) -> None:
    global _tagger
    _tagger = tagger


@app.get("/health")
def health() -> dict:
    return {"status": "ok", "llm_mode": get_config()["llm"]["mode"], "prompt_version": PROMPT_VERSION}


@app.post("/tag", response_model=TagResult)
def tag(event: TagInput) -> TagResult:
    try:
        return get_tagger().tag_event(event)
    except (BudgetExceeded, LLMNetworkError) as e:
        raise HTTPException(503, f"LLM недоступен: {type(e).__name__}") from None
    except LLMError as e:
        raise HTTPException(502, f"ошибка LLM: {type(e).__name__}") from None


@app.post("/tag/batch", response_model=list[TagResult])
def tag_batch(events: list[TagInput]) -> list[TagResult]:
    out = []
    for ev in events:
        try:
            out.append(get_tagger().tag_event(ev))
        except LLMError as e:                      # один сбой не роняет пачку: событие вернётся без Event
            log.error("event %s: %s", ev.event_id, type(e).__name__)
            out.append(TagResult(noise_reason=f"ошибка LLM: {type(e).__name__}"))
    return out
