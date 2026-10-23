"""OpenAI-совместимый клиент: POST {LLM_BASE_URL}/chat/completions. Ключ только из LLM_API_KEY, в логи и ошибки не попадает
(любое sk-... заменяется на sk-***). При сетевой ошибке или 5xx основного адреса запрос один раз уходит на резервный
(LLM_BASE_URL_RESERVE). Ответ «Budget has been exceeded» -> BudgetExceeded: без повтора и без резервного адреса, бюджет общий на ключ."""
from __future__ import annotations

import os

import httpx
from pydantic import BaseModel

from .base import BudgetExceeded, LLMClient, LLMError, LLMNetworkError, RawResponse, Store, estimate_tokens, log

BUDGET_TEXT = "budget has been exceeded"        # искать в теле ответа без учёта регистра; код HTTP шлюза неизвестен


class HttpLLM(LLMClient):
    def __init__(self, cfg: dict, store: Store | None = None, client: httpx.Client | None = None):
        super().__init__(cfg, store)
        llm = cfg["llm"]
        self.base_url = (llm.get("base_url") or os.environ.get("LLM_BASE_URL", "")).rstrip("/")
        self.reserve_url = (llm.get("base_url_reserve") or os.environ.get("LLM_BASE_URL_RESERVE", "")).rstrip("/")
        self.disable_reasoning = set(llm.get("disable_reasoning") or [])
        self.disable_reasoning_param: dict = llm.get("disable_reasoning_param") or {}
        key = os.environ.get("LLM_API_KEY", "")
        if not self.base_url:
            raise LLMError("LLM_BASE_URL не задан (нужен для LLM_MODE=http)")
        if not key:
            raise LLMError("LLM_API_KEY не задан (ключ берётся только из переменных окружения)")
        self._endpoints = [("primary", self.base_url)]
        if self.reserve_url and self.reserve_url != self.base_url:
            self._endpoints.append(("reserve", self.reserve_url))
        self._headers = {"Authorization": f"Bearer {key}"}
        self._http = client or httpx.Client(timeout=float(llm.get("timeout_s", 60)))

    def _request(self, method: str, path: str, *, root: bool = False, **kw) -> tuple[dict, str]:
        """(JSON ответа, "primary" | "reserve"). Сеть/5xx основного адреса -> один заход на резервный.
        root=True: путь от корня хоста, без суффикса /v1 (так устроен /key/info)."""
        failures: list[str] = []
        for name, base in self._endpoints:
            try:
                url = (base[:-3] if root and base.endswith("/v1") else base) + path
                r = self._http.request(method, url, headers=self._headers, **kw)
            except httpx.TimeoutException as e:
                failures.append(f"{name}: таймаут ({type(e).__name__})")
            except httpx.TransportError as e:
                failures.append(f"{name}: сбой сети ({type(e).__name__})")
            else:
                if BUDGET_TEXT in r.text.lower():
                    raise BudgetExceeded(f"шлюз: Budget has been exceeded (HTTP {r.status_code})")
                if r.status_code < 500:
                    return self._parse(r), name
                failures.append(f"{name}: HTTP {r.status_code}")
            if name == "primary" and len(self._endpoints) > 1:
                log.warning("основной адрес недоступен (%s), запрос уходит на резервный", failures[-1])
        raise LLMNetworkError("; ".join(failures))

    @staticmethod
    def _parse(r: httpx.Response) -> dict:
        if r.status_code == 429:
            raise LLMNetworkError("HTTP 429")
        if r.status_code >= 400:
            raise LLMError(f"HTTP {r.status_code}: {r.text[:200]}")
        try:
            return r.json()
        except ValueError:
            raise LLMError("ответ сервера не JSON") from None

    def call_raw(self, model: str, messages: list[dict], schema: type[BaseModel] | None = None,
                 task: str | None = None) -> RawResponse:
        body: dict = {"model": model, "messages": messages, "temperature": 0}
        if model in self.disable_reasoning:
            body.update(self.disable_reasoning_param)         # TODO(align): формат параметра у шлюза не известен
        data, endpoint = self._request("POST", "/chat/completions", json=body)
        try:
            text = data["choices"][0]["message"]["content"]
        except (KeyError, IndexError, TypeError):
            raise LLMError("неожиданный формат ответа (TODO(align): ожидается choices[0].message.content)") from None
        usage = data.get("usage") or {}
        if not (usage.get("prompt_tokens") and usage.get("completion_tokens")):
            log.warning("в ответе %s нет usage, токены оценены по длине текста", model)
        tin = usage.get("prompt_tokens") or sum(estimate_tokens(m.get("content", "")) for m in messages)
        tout = usage.get("completion_tokens") or estimate_tokens(text)      # токены рассуждений уже входят в completion_tokens
        return RawResponse(text, int(tin), int(tout), endpoint)

    def list_models(self) -> list[str]:
        data, _ = self._request("GET", "/models")          # TODO(align): ожидается {"data": [{"id": ...}]}
        return sorted(str(m.get("id")) for m in data.get("data", []))

    def key_info(self) -> dict:
        return self._request("GET", "/key/info", root=True)[0]

    def close(self) -> None:
        self._http.close()
