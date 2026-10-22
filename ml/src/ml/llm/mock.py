"""MockLLM: детерминированные валидные ответы по задаче и хешу входа, без сети и ключа.
Сценарий: список ответов, которые выдаются по очереди; str - сырой ответ, None - обычный валидный ответ,
исключение - будет поднято (например, LLMNetworkError). Пример «первый невалидный, второй валидный»: [INVALID, None]."""
from __future__ import annotations

import hashlib
import json
from collections import deque

from pydantic import BaseModel

from .base import LLMClient, RawResponse, Store, estimate_tokens

INVALID = "это не JSON {"


def _gen(node: dict, defs: dict, seed: int):
    """Минимальный валидный пример по JSON Schema (только обязательные поля), детерминированно от seed."""
    if "$ref" in node:
        return _gen(defs[node["$ref"].split("/")[-1]], defs, seed)
    if "enum" in node:
        return node["enum"][seed % len(node["enum"])]
    if "const" in node:
        return node["const"]
    if "anyOf" in node:
        opts = [o for o in node["anyOf"] if o.get("type") != "null"]
        return _gen(opts[0], defs, seed) if opts else None
    t = node.get("type")
    if t == "object":
        props = node.get("properties", {})
        return {k: _gen(props[k], defs, seed + i) for i, k in enumerate(node.get("required", []))}
    if t == "string":
        return f"mock-{seed % 1000:03d}"
    if t in ("integer", "number"):
        lo = node.get("minimum", 0)
        hi = node.get("maximum", lo + 9)
        v = lo + seed % (int(hi - lo) + 1)
        return v if t == "integer" else float(v)
    if t == "boolean":
        return False
    if t == "array":
        return []
    return None


class MockLLM(LLMClient):
    def __init__(self, cfg: dict, store: Store | None = None, scenario: list | None = None):
        super().__init__(cfg, store)
        self.scenario = deque(scenario or [])
        self.calls = 0                       # счётчик «сетевых» вызовов
        self.requests: list[list[dict]] = []

    def call_raw(self, model: str, messages: list[dict], schema: type[BaseModel] | None = None,
                 task: str | None = None) -> RawResponse:
        self.calls += 1
        self.requests.append(messages)
        tokens_in = sum(estimate_tokens(m.get("content", "")) for m in messages)
        item = self.scenario.popleft() if self.scenario else None
        if isinstance(item, BaseException):
            raise item
        text = item if item is not None else self._default(task, messages, schema)
        return RawResponse(text, tokens_in, estimate_tokens(text))

    def list_models(self) -> list[str]:
        return sorted(set(self.models.values()))

    @staticmethod
    def _default(task: str | None, messages: list[dict], schema: type[BaseModel] | None) -> str:
        if schema is None:
            return "ок" if task is None else json.dumps({"ok": True, "task": task})
        seed = int(hashlib.sha256((str(task) + json.dumps(messages, ensure_ascii=False, sort_keys=True)).encode()).hexdigest()[:8], 16)
        js = schema.model_json_schema()
        return json.dumps(_gen(js, js.get("$defs", {}), seed), ensure_ascii=False)
