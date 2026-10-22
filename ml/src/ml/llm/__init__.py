from ..config import get_config
from .base import (
    Budget,
    BudgetExceeded,
    DegradedMode,
    LLMClient,
    LLMError,
    LLMFormatError,
    LLMNetworkError,
    RawResponse,
    Store,
    cache_key,
)
from .http import HttpLLM
from .mock import INVALID, MockLLM

__all__ = ["Budget", "BudgetExceeded", "DegradedMode", "HttpLLM", "INVALID", "LLMClient", "LLMError", "LLMFormatError",
           "LLMNetworkError", "MockLLM", "RawResponse", "Store", "build_llm", "cache_key"]


def build_llm(cfg: dict | None = None, store: Store | None = None) -> LLMClient:
    """Клиент по llm.mode (mock | http; переопределяется LLM_MODE)."""
    cfg = cfg or get_config()
    mode = cfg["llm"]["mode"]
    if mode == "mock":
        return MockLLM(cfg, store)
    if mode == "http":
        return HttpLLM(cfg, store)
    raise LLMError(f"неизвестный llm.mode: {mode!r} (mock | http)")
