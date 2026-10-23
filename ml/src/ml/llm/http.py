"""OpenAI-совместимый клиент: POST {LLM_BASE_URL}/chat/completions. Ключ только из LLM_API_KEY, в логи и ошибки не попадает.
TODO(align): формат запроса/ответа и названия моделей подтвердят организаторы."""
from __future__ import annotations

import os

import httpx
from pydantic import BaseModel

from .base import LLMClient, LLMError, LLMNetworkError, RawResponse, Store, estimate_tokens


class HttpLLM(LLMClient):
    def __init__(self, cfg: dict, store: Store | None = None, client: httpx.Client | None = None):
        super().__init__(cfg, store)
        self.base_url = (cfg["llm"].get("base_url") or os.environ.get("LLM_BASE_URL", "")).rstrip("/")
        key = os.environ.get("LLM_API_KEY", "")
        if not self.base_url:
            raise LLMError("LLM_BASE_URL не задан (нужен для LLM_MODE=http)")
        if not key:
            raise LLMError("LLM_API_KEY не задан (ключ берётся только из переменных окружения)")
        self._headers = {"Authorization": f"Bearer {key}"}
        self._http = client or httpx.Client(timeout=float(cfg["llm"].get("timeout_s", 60)))

    def _request(self, method: str, path: str, **kw) -> dict:
        try:
            r = self._http.request(method, f"{self.base_url}{path}", headers=self._headers, **kw)
        except httpx.TimeoutException as e:
            raise LLMNetworkError(f"таймаут ({type(e).__name__})") from None
        except httpx.TransportError as e:
            raise LLMNetworkError(f"сбой сети ({type(e).__name__})") from None
        if r.status_code >= 500 or r.status_code == 429:
            raise LLMNetworkError(f"HTTP {r.status_code}")
        if r.status_code >= 400:
            raise LLMError(f"HTTP {r.status_code}: {r.text[:200]}")
        try:
            return r.json()
        except ValueError:
            raise LLMError("ответ сервера не JSON") from None

    def call_raw(self, model: str, messages: list[dict], schema: type[BaseModel] | None = None,
                 task: str | None = None) -> RawResponse:
        data = self._request("POST", "/chat/completions", json={"model": model, "messages": messages, "temperature": 0})
        try:
            text = data["choices"][0]["message"]["content"]
        except (KeyError, IndexError, TypeError):
            raise LLMError("неожиданный формат ответа (TODO(align): ожидается choices[0].message.content)") from None
        usage = data.get("usage") or {}
        tin = usage.get("prompt_tokens") or sum(estimate_tokens(m.get("content", "")) for m in messages)
        tout = usage.get("completion_tokens") or estimate_tokens(text)
        return RawResponse(text, int(tin), int(tout))

    def list_models(self) -> list[str]:
        data = self._request("GET", "/models")          # TODO(align): ожидается {"data": [{"id": ...}]}
        return sorted(str(m.get("id")) for m in data.get("data", []))

    def close(self) -> None:
        self._http.close()
