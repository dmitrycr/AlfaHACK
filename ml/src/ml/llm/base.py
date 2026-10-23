"""Порт LLMClient: модель по задаче, кеш sha256(model + messages), журнал вызовов, дневной бюджет, повторы.
Один SQLite-файл (paths.store): таблицы llm_cache и llm_calls. Ключ API здесь не появляется (его читает только http.py из окружения)."""
from __future__ import annotations

import hashlib
import json
import logging
import re
import sqlite3
import threading
import time
from abc import ABC, abstractmethod
from contextlib import closing
from dataclasses import dataclass
from datetime import date, datetime
from pathlib import Path
from typing import Any

from pydantic import BaseModel

from ..config import resolve_path

log = logging.getLogger("ml.llm")

_FENCE = re.compile(r"^```(?:json)?\s*(.*?)\s*```$", re.S)

_SQL = """
CREATE TABLE IF NOT EXISTS llm_cache (key TEXT PRIMARY KEY, response TEXT NOT NULL, created TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS llm_calls (
    id INTEGER PRIMARY KEY AUTOINCREMENT, ts TEXT NOT NULL, day TEXT NOT NULL, run_id TEXT, task TEXT, model TEXT,
    prompt_version TEXT, tokens_in INTEGER, tokens_out INTEGER, duration_ms INTEGER, status TEXT, cost_rub REAL,
    cached INTEGER, error TEXT);
CREATE INDEX IF NOT EXISTS ix_llm_calls_day ON llm_calls(day);
CREATE TABLE IF NOT EXISTS kv (ns TEXT NOT NULL, key TEXT NOT NULL, value TEXT NOT NULL, PRIMARY KEY (ns, key));
"""


class LLMError(Exception):
    """Любая ошибка LLM-клиента."""


class LLMNetworkError(LLMError):
    """Сбой сети, таймаут, HTTP 5xx/429: клиент повторяет вызов."""


class LLMFormatError(LLMError):
    """Ответ не JSON или не проходит схему даже после повтора."""


class BudgetExceeded(LLMError):
    """Дневной лимит исчерпан (>= 100%): вызов без кеша не уходит в сеть."""


class DegradedMode(BudgetExceeded):
    """Режим деградации (>= degrade_at, но < 100%): отвечаем только из кеша."""


@dataclass
class RawResponse:
    text: str
    tokens_in: int = 0
    tokens_out: int = 0


def cache_key(model: str, messages: list[dict]) -> str:
    return hashlib.sha256((model + json.dumps(messages, ensure_ascii=False, sort_keys=True)).encode("utf-8")).hexdigest()


def estimate_tokens(text: str) -> int:
    return max(1, len(text) // 4)


def parse_json(text: str, schema: type[BaseModel] | None) -> Any:
    """JSON (допускается обёртка ```json) -> объект схемы или dict. ValueError, если не JSON или не по схеме."""
    t = text.strip()
    m = _FENCE.match(t)
    if m:
        t = m.group(1)
    data = json.loads(t)
    return schema.model_validate(data) if schema is not None else data


class Store:
    """Кеш ответов и журнал вызовов. Соединение на каждую операцию: безопасно для нескольких потоков."""

    def __init__(self, path: str | Path):
        self.path = Path(path)
        self.path.parent.mkdir(parents=True, exist_ok=True)
        self._lock = threading.Lock()
        with self._lock, closing(sqlite3.connect(self.path, timeout=30)) as c:
            c.executescript(_SQL)

    def _exec(self, sql: str, params: tuple = ()) -> list[tuple]:
        with self._lock, closing(sqlite3.connect(self.path, timeout=30)) as c:
            rows = c.execute(sql, params).fetchall()
            c.commit()
            return rows

    def cache_get(self, key: str) -> str | None:
        rows = self._exec("SELECT response FROM llm_cache WHERE key=?", (key,))
        return rows[0][0] if rows else None

    def cache_put(self, key: str, response: str) -> None:
        self._exec("INSERT OR REPLACE INTO llm_cache(key, response, created) VALUES (?,?,?)",
                   (key, response, datetime.now().isoformat(timespec="seconds")))

    def log_call(self, *, run_id: str | None, task: str | None, model: str | None, prompt_version: str | None,
                 tokens_in: int = 0, tokens_out: int = 0, duration_ms: int = 0, status: str = "ok",
                 cost_rub: float = 0.0, cached: bool = False, error: str | None = None) -> None:
        now = datetime.now()
        self._exec("INSERT INTO llm_calls(ts, day, run_id, task, model, prompt_version, tokens_in, tokens_out, duration_ms,"
                   " status, cost_rub, cached, error) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)",
                   (now.isoformat(timespec="seconds"), now.date().isoformat(), run_id, task, model, prompt_version,
                    tokens_in, tokens_out, duration_ms, status, cost_rub, int(cached), error))

    def kv_get(self, ns: str, key: str) -> str | None:
        rows = self._exec("SELECT value FROM kv WHERE ns=? AND key=?", (ns, key))
        return rows[0][0] if rows else None

    def kv_put(self, ns: str, key: str, value: str) -> None:
        self._exec("INSERT OR REPLACE INTO kv(ns, key, value) VALUES (?,?,?)", (ns, key, value))

    def journal(self, since_id: int = 0) -> list[dict]:
        cols = ["id", "ts", "day", "run_id", "task", "model", "prompt_version", "tokens_in", "tokens_out", "duration_ms",
                "status", "cost_rub", "cached", "error"]
        return [dict(zip(cols, r)) for r in
                self._exec(f"SELECT {', '.join(cols)} FROM llm_calls WHERE id>? ORDER BY id", (since_id,))]

    def spent(self, day: date | None = None) -> float:
        d = (day or date.today()).isoformat()
        return float(self._exec("SELECT COALESCE(SUM(cost_rub), 0) FROM llm_calls WHERE day=?", (d,))[0][0])


class Budget:
    """normal -> degraded (>= degrade_at от лимита) -> exceeded (>= 100%). Расход берётся из журнала за сегодня."""

    def __init__(self, store: Store, limit_rub: float, degrade_at: float = 0.8):
        self.store, self.limit_rub, self.degrade_at = store, limit_rub, degrade_at

    def spent_today(self) -> float:
        return self.store.spent()

    def state(self) -> str:
        spent = self.spent_today()
        if spent >= self.limit_rub:
            return "exceeded"
        if spent >= self.limit_rub * self.degrade_at:
            return "degraded"
        return "normal"


class LLMClient(ABC):
    def __init__(self, cfg: dict, store: Store | None = None):
        llm = cfg["llm"]
        self.models: dict[str, str] = llm["models"]
        self.prices: dict = llm.get("prices") or {}
        self.net_retries = int(llm.get("net_retries", 2))
        self.retry_pause_s = float(llm.get("retry_pause_s", 1.0))
        self.store = store or Store(resolve_path(cfg["paths"]["store"]))
        self.budget = Budget(self.store, float(llm["daily_limit_rub"]), float(llm.get("degrade_at", 0.8)))
        self._price_warned: set[str] = set()
        self.api_calls = 0                   # сколько раз реально вызывали модель (включая повторы)

    @abstractmethod
    def call_raw(self, model: str, messages: list[dict], schema: type[BaseModel] | None = None,
                 task: str | None = None) -> RawResponse:
        """Один запрос к модели без кеша и повторов. Сетевые сбои -> LLMNetworkError."""

    def list_models(self) -> list[str] | None:
        """Список моделей, которые видит ключ (None, если узнать нельзя)."""
        return None

    def model_for(self, task: str) -> str:
        try:
            return self.models[task]
        except KeyError:
            raise LLMError(f"неизвестная задача '{task}', доступны: {sorted(self.models)}") from None

    def cost(self, model: str, tokens_in: int, tokens_out: int) -> float:
        price = self.prices.get(model)
        if not price:
            if model not in self._price_warned:
                self._price_warned.add(model)
                log.warning("TODO(align): цены токенов для модели %s не заданы (llm.prices), стоимость = 0, "
                            "бюджет не расходуется", model)
            return 0.0
        return tokens_in / 1000 * float(price.get("in", 0)) + tokens_out / 1000 * float(price.get("out", 0))

    def complete(self, task: str, messages: list[dict], schema: type[BaseModel] | None = None,
                 run_id: str | None = None, *, prompt_version: str | None = None) -> Any:
        """Объект схемы (если schema задана) или dict. Кеш -> бюджет -> вызов -> проверка JSON (один повтор)."""
        model = self.model_for(task)
        key = cache_key(model, messages)
        ctx = dict(run_id=run_id, task=task, model=model, prompt_version=prompt_version)

        cached = self.store.cache_get(key)
        if cached is not None:
            try:
                result = parse_json(cached, schema)
            except ValueError:
                log.warning("битая запись кеша %s..., игнорируется", key[:12])
            else:
                self.store.log_call(**ctx, status="ok", cached=True)
                return result

        msgs = messages
        for attempt in (0, 1):
            raw, ms, cost = self._call_with_net_retries(model, msgs, schema, ctx)
            try:
                result = parse_json(raw.text, schema)
            except ValueError as e:
                err = str(e)[:300]
                final = attempt == 1
                self.store.log_call(**ctx, tokens_in=raw.tokens_in, tokens_out=raw.tokens_out, duration_ms=ms,
                                    status="error" if final else "retry", cost_rub=cost, error=f"невалидный JSON: {err}")
                if final:
                    raise LLMFormatError(f"ответ модели {model} не прошёл проверку после повтора: {err}") from e
                msgs = messages + [
                    {"role": "assistant", "content": raw.text},
                    {"role": "user", "content": f"Ответ не прошёл проверку: {err}. Верни ТОЛЬКО валидный JSON по схеме, без пояснений."},
                ]
                continue
            self.store.log_call(**ctx, tokens_in=raw.tokens_in, tokens_out=raw.tokens_out, duration_ms=ms,
                                status="ok", cost_rub=cost)
            self.store.cache_put(key, result.model_dump_json() if isinstance(result, BaseModel)
                                 else json.dumps(result, ensure_ascii=False))
            return result
        raise AssertionError("unreachable")

    def _guard(self, ctx: dict) -> None:
        state = self.budget.state()
        if state == "normal":
            return
        spent, limit = self.budget.spent_today(), self.budget.limit_rub
        if state == "exceeded":
            msg = f"дневной лимит {limit:g} ₽ исчерпан (потрачено {spent:.2f} ₽)"
            exc: BudgetExceeded = BudgetExceeded(msg)
        else:
            msg = f"режим деградации: потрачено {spent:.2f} из {limit:g} ₽, ответ только из кеша"
            exc = DegradedMode(msg)
        self.store.log_call(**ctx, status="error", error=msg)
        raise exc

    def _call_with_net_retries(self, model: str, messages: list[dict], schema, ctx: dict) -> tuple[RawResponse, int, float]:
        for attempt in range(self.net_retries + 1):
            self._guard(ctx)
            self.api_calls += 1
            t0 = time.perf_counter()
            try:
                raw = self.call_raw(model, messages, schema, ctx["task"])
            except LLMNetworkError as e:
                ms = int((time.perf_counter() - t0) * 1000)
                last = attempt == self.net_retries
                self.store.log_call(**ctx, duration_ms=ms, status="error" if last else "retry", error=str(e)[:300])
                if last:
                    raise
                log.warning("сбой сети (%s), повтор %d из %d", e, attempt + 1, self.net_retries)
                if self.retry_pause_s > 0:
                    time.sleep(self.retry_pause_s)
                continue
            ms = int((time.perf_counter() - t0) * 1000)
            return raw, ms, self.cost(model, raw.tokens_in, raw.tokens_out)
        raise AssertionError("unreachable")
