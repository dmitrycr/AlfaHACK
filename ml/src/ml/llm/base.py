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
from datetime import date, datetime, timedelta, timezone
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
    cached INTEGER, error TEXT, endpoint TEXT);
CREATE INDEX IF NOT EXISTS ix_llm_calls_day ON llm_calls(day);
CREATE TABLE IF NOT EXISTS kv (ns TEXT NOT NULL, key TEXT NOT NULL, value TEXT NOT NULL, PRIMARY KEY (ns, key));
"""


_SECRET = re.compile(r"sk-[A-Za-z0-9_\-]+")

MSK = timezone(timedelta(hours=3), "MSK")
RESET_HOUR = 3                                  # дневной бюджет команды сбрасывается в 03:00 МСК


def mask_secrets(text: str) -> str:
    """Ключи вида sk-... -> sk-***: в журналах, логах и ошибках ключа быть не должно."""
    return _SECRET.sub("sk-***", text)


class _MaskFilter(logging.Filter):
    def filter(self, record: logging.LogRecord) -> bool:
        record.msg, record.args = mask_secrets(record.getMessage()), ()
        return True


log.addFilter(_MaskFilter())


def parse_key_info(data: dict) -> dict | None:
    """Ответ /key/info {"key": ..., "info": {"spend", "max_budget", "budget_reset_at", ...}} -> spend, max_budget, remaining,
    reset_at (МСК). Поле "key" не читается. None, если spend/max_budget нет."""
    info = (data or {}).get("info") or {}
    spend, limit = info.get("spend"), info.get("max_budget")
    if not isinstance(spend, (int, float)) or not isinstance(limit, (int, float)):
        return None
    reset = None
    if info.get("budget_reset_at"):
        try:
            reset = datetime.fromisoformat(str(info["budget_reset_at"]).replace("Z", "+00:00")).astimezone(MSK)
        except ValueError:
            pass
    return {"spend": float(spend), "max_budget": float(limit), "remaining": float(limit) - float(spend), "reset_at": reset}


def compare_spend(gateway_drop: float, our_rub: float) -> str:
    """Сверка роста spend у шлюза с нашей оценкой: вывод о единице тарифа."""
    if our_rub <= 0 or gateway_drop <= 0:
        return "сравнить нельзя: расход шлюза или наша оценка равны нулю (учёт у шлюза может идти с задержкой)"
    ratio = gateway_drop / our_rub
    if 0.5 <= ratio <= 2:
        return f"шлюз/наш = {ratio:.3g}: совпадает по порядку величины, единица тарифа верна"
    return (f"шлюз/наш = {ratio:.3g}: НЕ совпадает, шлюз считает единицу иначе"
            + (" (похоже на цены за 1 тыс. токенов)" if 500 <= ratio <= 2000 else "")
            + ("; spend может быть не в ₽" if ratio < 0.5 or ratio > 2000 else ""))


def now_msk() -> datetime:
    return datetime.now(MSK)


def budget_day(now: datetime | None = None) -> date:
    """Дата бюджетного окна: окно идёт с 03:00 МСК до 03:00 МСК следующих суток."""
    return ((now or now_msk()).astimezone(MSK) - timedelta(hours=RESET_HOUR)).date()


def next_reset(now: datetime | None = None) -> datetime:
    """Ближайший сброс бюджета (03:00 МСК) строго после now."""
    now = (now or now_msk()).astimezone(MSK)
    t = now.replace(hour=RESET_HOUR, minute=0, second=0, microsecond=0)
    return t if t > now else t + timedelta(days=1)


class LLMError(Exception):
    """Любая ошибка LLM-клиента. Текст очищается от ключей sk-..."""

    def __init__(self, *args):
        super().__init__(*(mask_secrets(a) if isinstance(a, str) else a for a in args))


class LLMNetworkError(LLMError):
    """Сбой сети, таймаут, HTTP 5xx/429: клиент повторяет вызов."""


class LLMFormatError(LLMError):
    """Ответ не JSON или не проходит схему даже после повтора."""


class BudgetExceeded(LLMError):
    """Дневной лимит исчерпан (>= 100%) или шлюз ответил "Budget has been exceeded": вызов без кеша не уходит в сеть."""


class DegradedMode(BudgetExceeded):
    """Режим деградации (>= degrade_at, но < 100%): отвечаем только из кеша."""


@dataclass
class RawResponse:
    text: str
    tokens_in: int = 0
    tokens_out: int = 0
    endpoint: str = ""                   # какой адрес ответил: primary | reserve ("" у мока)


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
            if "endpoint" not in {r[1] for r in c.execute("PRAGMA table_info(llm_calls)")}:    # кеш прежних версий
                c.execute("ALTER TABLE llm_calls ADD COLUMN endpoint TEXT")
                c.commit()

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
                 cost_rub: float = 0.0, cached: bool = False, error: str | None = None,
                 endpoint: str | None = None) -> None:
        now = now_msk()
        self._exec("INSERT INTO llm_calls(ts, day, run_id, task, model, prompt_version, tokens_in, tokens_out, duration_ms,"
                   " status, cost_rub, cached, error, endpoint) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)",
                   (now.isoformat(timespec="seconds"), budget_day(now).isoformat(), run_id, task, model, prompt_version,
                    tokens_in, tokens_out, duration_ms, status, cost_rub, int(cached),
                    mask_secrets(error) if error else error, endpoint or None))

    def kv_get(self, ns: str, key: str) -> str | None:
        rows = self._exec("SELECT value FROM kv WHERE ns=? AND key=?", (ns, key))
        return rows[0][0] if rows else None

    def kv_put(self, ns: str, key: str, value: str) -> None:
        self._exec("INSERT OR REPLACE INTO kv(ns, key, value) VALUES (?,?,?)", (ns, key, value))

    def journal(self, since_id: int = 0) -> list[dict]:
        cols = ["id", "ts", "day", "run_id", "task", "model", "prompt_version", "tokens_in", "tokens_out", "duration_ms",
                "status", "cost_rub", "cached", "error", "endpoint"]
        return [dict(zip(cols, r)) for r in
                self._exec(f"SELECT {', '.join(cols)} FROM llm_calls WHERE id>? ORDER BY id", (since_id,))]

    def spent(self, day: date | None = None) -> float:
        d = (day or budget_day()).isoformat()
        return float(self._exec("SELECT COALESCE(SUM(cost_rub), 0) FROM llm_calls WHERE day=?", (d,))[0][0])


class Budget:
    """normal -> degraded (>= degrade_at от лимита) -> exceeded (>= 100% или шлюз сообщил об исчерпании бюджета).
    Расход берётся из журнала за текущее окно (с 03:00 МСК). После ответа шлюза «Budget has been exceeded»
    клиент работает только из кеша до ближайших 03:00 МСК (метка лежит в kv, переживает перезапуск)."""

    def __init__(self, store: Store, limit_rub: float, degrade_at: float = 0.8):
        self.store, self.limit_rub, self.degrade_at = store, limit_rub, degrade_at

    def spent_today(self) -> float:
        return self.store.spent()

    def blocked_until(self) -> datetime | None:
        raw = self.store.kv_get("budget", "cache_only_until")
        until = datetime.fromisoformat(raw) if raw else None
        return until if until and until > now_msk() else None

    def block(self) -> datetime:
        until = next_reset()
        self.store.kv_put("budget", "cache_only_until", until.isoformat())
        return until

    def state(self) -> str:
        if self.blocked_until():
            return "exceeded"
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
        self.price_unit_tokens = float(llm.get("price_unit_tokens") or 1_000_000)   # цены в prices - за 1 млн токенов
        self.card_model_final: str = (llm.get("card_model_final") or "") if llm.get("final_run") else ""
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

    def key_info(self) -> dict | None:
        """Ответ GET {host}/key/info (адрес без /v1); None, если узнать нельзя. Разбор: parse_key_info."""
        return None

    def model_for(self, task: str) -> str:
        if task == "card" and self.card_model_final:
            return self.card_model_final
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
        # токены рассуждений входят в tokens_out и оплачиваются по цене выхода
        return (tokens_in * float(price.get("in", 0)) + tokens_out * float(price.get("out", 0))) / self.price_unit_tokens

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
                                    status="error" if final else "retry", cost_rub=cost, error=f"невалидный JSON: {err}",
                                    endpoint=raw.endpoint)
                if final:
                    raise LLMFormatError(f"ответ модели {model} не прошёл проверку после повтора: {err}") from e
                msgs = messages + [
                    {"role": "assistant", "content": raw.text},
                    {"role": "user", "content": f"Ответ не прошёл проверку: {err}. Верни ТОЛЬКО валидный JSON по схеме, без пояснений."},
                ]
                continue
            self.store.log_call(**ctx, tokens_in=raw.tokens_in, tokens_out=raw.tokens_out, duration_ms=ms,
                                status="ok", cost_rub=cost, endpoint=raw.endpoint)
            self.store.cache_put(key, result.model_dump_json() if isinstance(result, BaseModel)
                                 else json.dumps(result, ensure_ascii=False))
            return result
        raise AssertionError("unreachable")

    def _guard(self, ctx: dict) -> None:
        state = self.budget.state()
        if state == "normal":
            return
        spent, limit = self.budget.spent_today(), self.budget.limit_rub
        until = self.budget.blocked_until()
        if until:
            msg = f"шлюз сообщил об исчерпании бюджета: только кеш до {until:%d.%m.%Y %H:%M} МСК"
            exc: BudgetExceeded = BudgetExceeded(msg)
        elif state == "exceeded":
            msg = f"дневной лимит {limit:g} ₽ исчерпан (потрачено {spent:.2f} ₽)"
            exc = BudgetExceeded(msg)
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
            except BudgetExceeded as e:           # ответ шлюза: бюджет общий на ключ, ни повтора, ни резервного адреса
                ms = int((time.perf_counter() - t0) * 1000)
                until = self.budget.block()
                msg = f"{e}; только кеш до {until:%d.%m.%Y %H:%M} МСК"
                log.error("бюджет исчерпан по ответу шлюза: %s", msg)
                self.store.log_call(**ctx, duration_ms=ms, status="error", error=msg[:300])
                raise BudgetExceeded(msg) from None
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
