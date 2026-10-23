import json
import logging
import sqlite3
from datetime import datetime, timezone

import httpx
import pytest

from ml.llm.base import MSK, Budget, budget_day, mask_secrets, next_reset
from ml.llm import (INVALID, BudgetExceeded, DegradedMode, HttpLLM, LLMError, LLMFormatError, LLMNetworkError, MockLLM,
                    Store, build_llm, cache_key)
from ml.schemas import EventTags

MSG = [{"role": "user", "content": "Новость: ЦБ сохранил ставку"}]


def test_cache_second_call_does_not_hit_network(cfg):
    llm = MockLLM(cfg)
    a = llm.complete("tagging", MSG, EventTags, run_id="r1")
    b = llm.complete("tagging", MSG, EventTags, run_id="r1")
    assert llm.calls == 1 and a == b and isinstance(a, EventTags)
    assert [(j["status"], j["cached"]) for j in llm.store.journal()] == [("ok", 0), ("ok", 1)]
    llm.complete("tagging", MSG + [{"role": "user", "content": "ещё"}], EventTags)   # другие messages -> промах
    assert llm.calls == 2


def test_cache_persists_between_clients_and_key_includes_model(cfg):
    MockLLM(cfg).complete("tagging", MSG, EventTags)
    second = MockLLM(cfg)
    second.complete("tagging", MSG, EventTags)
    assert second.calls == 0
    second.complete("card", MSG, EventTags)                       # другая модель -> другой ключ
    assert second.calls == 1
    assert cache_key("a", MSG) != cache_key("b", MSG)


def test_invalid_json_exactly_one_retry(cfg):
    llm = MockLLM(cfg, scenario=[INVALID, None])
    res = llm.complete("tagging", MSG, EventTags, run_id="r", prompt_version="tagging_v1")
    assert isinstance(res, EventTags) and llm.calls == 2
    assert "не прошёл проверку" in llm.requests[1][-1]["content"]          # повтор содержит текст ошибки
    assert [j["status"] for j in llm.store.journal()] == ["retry", "ok"]
    assert llm.store.journal()[0]["prompt_version"] == "tagging_v1"


def test_invalid_twice_raises_format_error(cfg):
    llm = MockLLM(cfg, scenario=[INVALID, '{"importance": 99}', None])   # второй: JSON, но не по схеме
    with pytest.raises(LLMFormatError):
        llm.complete("tagging", MSG, EventTags)
    assert llm.calls == 2                                           # ровно один повтор
    assert llm.store.cache_get(cache_key(llm.model_for("tagging"), MSG)) is None   # невалидное в кеш не попадает


def test_network_retries_up_to_two(cfg):
    llm = MockLLM(cfg, scenario=[LLMNetworkError("x"), LLMNetworkError("x"), None])
    assert isinstance(llm.complete("tagging", MSG, EventTags), EventTags) and llm.calls == 3
    llm = MockLLM(cfg, scenario=[LLMNetworkError("x")] * 3)
    with pytest.raises(LLMNetworkError):
        llm.complete("tagging", [{"role": "user", "content": "другое"}], EventTags)
    assert llm.calls == 3


def test_budget_degraded_serves_only_cache(cfg):
    cfg["llm"]["daily_limit_rub"] = 1
    llm = MockLLM(cfg)
    llm.complete("tagging", MSG, EventTags)                         # закешировано, пока расход 0
    llm.store.log_call(run_id=None, task="x", model="m", prompt_version=None, cost_rub=0.85)   # 85% лимита 1 ₽
    assert llm.budget.state() == "degraded"
    before = llm.calls
    llm.complete("tagging", MSG, EventTags)                         # из кеша можно
    with pytest.raises(DegradedMode):
        llm.complete("tagging", [{"role": "user", "content": "новое"}], EventTags)   # без кеша - в сеть не уходит
    assert llm.calls == before


def test_budget_exceeded_at_100_percent(cfg):
    cfg["llm"]["daily_limit_rub"] = 1
    llm = MockLLM(cfg)
    llm.store.log_call(run_id=None, task="x", model="m", prompt_version=None, cost_rub=1.0)
    assert llm.budget.state() == "exceeded"
    with pytest.raises(BudgetExceeded) as e:
        llm.complete("tagging", MSG, EventTags)
    assert not isinstance(e.value, DegradedMode) and llm.calls == 0


def test_cost_from_prices_spends_budget(cfg):
    cfg["llm"]["prices"] = {"google/gemma-4-26b-a4b-it": {"in": 100.0, "out": 100.0}}     # ₽ за price_unit_tokens
    cfg["llm"]["price_unit_tokens"] = 1000
    cfg["llm"]["daily_limit_rub"] = 1
    llm = MockLLM(cfg)
    llm.complete("tagging", MSG, EventTags)
    assert llm.store.journal()[0]["cost_rub"] > 0 and llm.budget.state() == "exceeded"


def test_no_prices_cost_zero_and_todo_warning(cfg, caplog):
    cfg["llm"]["prices"] = {}
    llm = MockLLM(cfg)
    with caplog.at_level(logging.WARNING, logger="ml.llm"):
        llm.complete("tagging", MSG, EventTags)
    assert llm.store.journal()[0]["cost_rub"] == 0 and "TODO(align)" in caplog.text


def test_unknown_task(cfg):
    with pytest.raises(LLMError):
        MockLLM(cfg).complete("nope", MSG)


def test_mock_is_deterministic_and_valid_for_every_task(cfg, tmp_path):
    a = MockLLM(cfg).complete("tagging", MSG, EventTags)
    b = MockLLM(cfg, Store(tmp_path / "other.sqlite")).complete("tagging", MSG, EventTags)
    assert a == b
    for task in ("tagging", "link_check", "card", "selfcheck", "chat"):
        assert isinstance(MockLLM(cfg, Store(tmp_path / f"{task}.sqlite")).complete(task, MSG, EventTags), EventTags)
    assert MockLLM(cfg).complete("chat", MSG) == {"ok": True, "task": "chat"}    # без схемы -> dict
    assert MockLLM(cfg).call_raw("m", [{"role": "user", "content": "Ответь одним словом: ок"}]).text == "ок"


def test_build_llm_mock_by_default(cfg):
    assert isinstance(build_llm(cfg), MockLLM)
    cfg["llm"]["mode"] = "weird"
    with pytest.raises(LLMError):
        build_llm(cfg)


def test_http_requires_key_and_base_url(cfg, monkeypatch):
    monkeypatch.delenv("LLM_API_KEY", raising=False)
    cfg["llm"]["base_url"] = "http://llm.test/v1"
    with pytest.raises(LLMError, match="LLM_API_KEY"):
        HttpLLM(cfg)
    monkeypatch.setenv("LLM_API_KEY", "sk-secret-123")
    cfg["llm"]["base_url"] = ""
    monkeypatch.delenv("LLM_BASE_URL", raising=False)
    with pytest.raises(LLMError, match="LLM_BASE_URL"):
        HttpLLM(cfg)


def test_http_client_request_format_and_no_key_leak(cfg, monkeypatch, caplog):
    monkeypatch.setenv("LLM_API_KEY", "sk-secret-123")
    cfg["llm"]["base_url"] = "http://llm.test/v1/"
    cfg["llm"]["base_url_reserve"] = ""                         # без резерва: 503 -> обычный повтор
    seen = []

    def handler(req: httpx.Request) -> httpx.Response:
        seen.append(req)
        if req.method == "GET":
            return httpx.Response(200, json={"data": [{"id": "google/gemma-4-26b-a4b-it"}]})
        body = json.loads(req.content)
        if len(seen) == 2:
            return httpx.Response(503)                              # сбой -> повтор
        assert body["model"] == "google/gemma-4-26b-a4b-it" and body["messages"] == MSG
        content = json.dumps({"event_id": "e", "title": "t", "type": "макро", "importance": 3})
        return httpx.Response(200, json={"choices": [{"message": {"content": content}}],
                                         "usage": {"prompt_tokens": 7, "completion_tokens": 5}})

    llm = HttpLLM(cfg, client=httpx.Client(transport=httpx.MockTransport(handler)))
    assert llm.list_models() == ["google/gemma-4-26b-a4b-it"]
    with caplog.at_level(logging.DEBUG):
        res = llm.complete("tagging", MSG, EventTags)
    assert res.importance == 3 and len(seen) == 3
    assert seen[1].url.path == "/v1/chat/completions" and seen[1].headers["authorization"] == "Bearer sk-secret-123"
    j = llm.store.journal()
    assert [r["status"] for r in j] == ["retry", "ok"] and (j[1]["tokens_in"], j[1]["tokens_out"]) == (7, 5)
    assert "sk-secret-123" not in caplog.text and "sk-secret-123" not in json.dumps(j)


# ---- настройка под API организаторов -------------------------------------------------------------------------

PRIMARY, RESERVE = "http://main.test/v1", "http://reserve.test/v1"
OK_TAGS = json.dumps({"event_id": "e", "title": "t", "type": "макро", "importance": 3})


def ok_response(usage=None):
    return httpx.Response(200, json={"choices": [{"message": {"content": OK_TAGS}}],
                                     "usage": usage or {"prompt_tokens": 1000, "completion_tokens": 2000}})


def http_llm(cfg, monkeypatch, handler):
    monkeypatch.setenv("LLM_API_KEY", "sk-secret-123")
    cfg["llm"]["base_url"], cfg["llm"]["base_url_reserve"] = PRIMARY, RESERVE
    llm = HttpLLM(cfg, client=httpx.Client(transport=httpx.MockTransport(handler)))
    llm.net_retries = 0
    return llm


def at(*args):
    return datetime(*args, tzinfo=MSK)


def test_config_models_and_prices_use_gateway_ids(cfg):
    models = cfg["llm"]["models"]
    assert models["tagging"] == models["link_check"] == models["selfcheck"] == "google/gemma-4-26b-a4b-it"
    assert (models["card"], models["chat"], models["fallback"]) == (
        "z-ai/glm-5.3-flash", "qwen/qwen3.6-35b-a3b", "deepseek/deepseek-v4.1-flash")
    assert all("/" in m for m in models.values()) and set(models.values()) <= set(cfg["llm"]["prices"])
    assert cfg["llm"]["price_unit_tokens"] == 1_000_000 and cfg["llm"]["card_model_final"] == "moonshotai/kimi-k2.6"
    assert MockLLM(cfg).model_for("card") == "z-ai/glm-5.3-flash"             # final_run выключен


def test_card_model_final_overrides_card_only(cfg):
    cfg["llm"]["final_run"] = True
    llm = MockLLM(cfg)
    assert llm.model_for("card") == "moonshotai/kimi-k2.6" and llm.model_for("tagging") == "google/gemma-4-26b-a4b-it"


def test_failover_to_reserve_on_5xx_and_network_error(cfg, monkeypatch, caplog):
    hosts = []

    def handler(req):
        hosts.append(req.url.host)
        if req.url.host == "main.test":
            if len(hosts) == 1:
                return httpx.Response(502)
            raise httpx.ConnectError("boom")
        return ok_response()

    llm = http_llm(cfg, monkeypatch, handler)
    with caplog.at_level(logging.WARNING, logger="ml.llm"):
        assert llm.complete("tagging", MSG, EventTags).importance == 3
        assert llm.complete("tagging", MSG + [{"role": "user", "content": "ещё"}], EventTags).importance == 3
    assert hosts == ["main.test", "reserve.test", "main.test", "reserve.test"]
    assert [j["endpoint"] for j in llm.store.journal()] == ["reserve", "reserve"]
    assert "резервный" in caplog.text


def test_primary_ok_uses_primary_and_no_switch_on_4xx(cfg, monkeypatch):
    hosts = []

    def handler(req):
        hosts.append(req.url.host)
        return ok_response() if len(hosts) == 1 else httpx.Response(400, text="bad request")

    llm = http_llm(cfg, monkeypatch, handler)
    llm.complete("tagging", MSG, EventTags)
    assert llm.store.journal()[0]["endpoint"] == "primary"
    with pytest.raises(LLMError, match="HTTP 400"):
        llm.complete("tagging", [{"role": "user", "content": "x"}], EventTags)
    assert hosts == ["main.test", "main.test"]


def test_both_endpoints_down_switches_once_per_request(cfg, monkeypatch):
    hosts = []

    def handler(req):
        hosts.append(req.url.host)
        return httpx.Response(503)

    llm = http_llm(cfg, monkeypatch, handler)
    with pytest.raises(LLMNetworkError, match="primary.*reserve"):
        llm.complete("tagging", MSG, EventTags)
    assert hosts == ["main.test", "reserve.test"]


def test_budget_window_starts_at_0300_msk():
    assert budget_day(at(2026, 10, 23, 2, 59)) == budget_day(at(2026, 10, 22, 12, 0))
    assert budget_day(at(2026, 10, 23, 3, 0)) == budget_day(at(2026, 10, 23, 23, 59))
    assert budget_day(at(2026, 10, 23, 3, 0)) != budget_day(at(2026, 10, 23, 2, 59))
    assert next_reset(at(2026, 10, 23, 2, 59)) == at(2026, 10, 23, 3, 0)
    assert next_reset(at(2026, 10, 23, 3, 0)) == at(2026, 10, 24, 3, 0)
    assert budget_day(at(2026, 10, 23, 0, 30).astimezone(timezone.utc)) == budget_day(at(2026, 10, 22, 20, 0))


def test_spent_resets_at_0300_msk(cfg, monkeypatch):
    cfg["llm"]["daily_limit_rub"] = 10
    llm = MockLLM(cfg)
    clock = {"now": at(2026, 10, 23, 2, 50)}
    monkeypatch.setattr("ml.llm.base.now_msk", lambda: clock["now"])
    llm.store.log_call(run_id=None, task="x", model="m", prompt_version=None, cost_rub=9.0)
    clock["now"] = at(2026, 10, 23, 2, 59)
    assert llm.budget.spent_today() == 9.0 and llm.budget.state() == "degraded"
    clock["now"] = at(2026, 10, 23, 3, 0)
    assert llm.budget.spent_today() == 0 and llm.budget.state() == "normal"
    assert llm.store.spent(budget_day(at(2026, 10, 22, 12, 0))) == 9.0


def test_cost_from_real_usage_in_rubles(cfg, monkeypatch):
    llm = http_llm(cfg, monkeypatch, lambda req: ok_response({"prompt_tokens": 1000, "completion_tokens": 2000}))
    llm.complete("tagging", MSG, EventTags)
    cost = llm.store.journal()[0]["cost_rub"]
    assert cost == pytest.approx((1000 * 4.48 + 2000 * 23.00) / 1_000_000)      # выход (с рассуждениями) по цене выхода
    assert llm.cost("moonshotai/kimi-k2.6", 10**6, 10**6) == pytest.approx(321.0)
    llm.price_unit_tokens = 1000
    assert llm.cost("z-ai/glm-5.3-flash", 1000, 1000) == pytest.approx(22.1)


def test_budget_error_no_retry_no_reserve_cache_only(cfg, monkeypatch, caplog):
    hosts = []

    def handler(req):
        hosts.append(req.url.host)
        return httpx.Response(402, json={"error": {"message": "BUDGET has been Exceeded"}})

    llm = http_llm(cfg, monkeypatch, handler)
    llm.net_retries = 2
    cached = {"event_id": "e", "title": "t", "type": "макро", "importance": 1}
    llm.store.cache_put(cache_key(llm.model_for("tagging"), MSG), json.dumps(cached))
    with caplog.at_level(logging.ERROR, logger="ml.llm"):
        with pytest.raises(BudgetExceeded) as e:
            llm.complete("tagging", [{"role": "user", "content": "новое"}], EventTags)
    assert hosts == ["main.test"] and not isinstance(e.value, DegradedMode)       # ни повтора, ни резервного адреса
    assert "только кеш до" in str(e.value) and "МСК" in str(e.value) and "только кеш" in caplog.text
    j = llm.store.journal()
    assert j[-1]["status"] == "error" and "Budget has been exceeded" in j[-1]["error"]
    # режим «только кеш» до 03:00 МСК: в сеть не ходим, кеш отдаём
    until = next_reset()
    assert llm.budget.blocked_until() == until and llm.budget.state() == "exceeded"
    assert llm.complete("tagging", MSG, EventTags).importance == 1
    with pytest.raises(BudgetExceeded, match="только кеш"):
        llm.complete("tagging", [{"role": "user", "content": "ещё новее"}], EventTags)
    assert hosts == ["main.test"]
    assert Budget(llm.store, 500).state() == "exceeded"                              # переживает перезапуск
    monkeypatch.setattr("ml.llm.base.now_msk", lambda: until)                        # 03:00 МСК: блокировка снята
    assert llm.budget.blocked_until() is None and llm.budget.state() == "normal"


@pytest.mark.parametrize("status", [200, 400, 429, 500])
def test_budget_error_matches_any_status_and_case(cfg, monkeypatch, status):
    calls = []

    def handler(req):
        calls.append(req.url.host)
        return httpx.Response(status, text="Error: budget HAS been exceeded for key")

    llm = http_llm(cfg, monkeypatch, handler)
    with pytest.raises(BudgetExceeded):
        llm.call_raw("google/gemma-4-26b-a4b-it", MSG)
    assert calls == ["main.test"]


def test_sk_keys_masked_in_journal_errors_and_logs(cfg, monkeypatch, caplog):
    assert mask_secrets("key sk-AbC_123-x and sk-***") == "key sk-*** and sk-***"

    def handler(req):
        return httpx.Response(401, text='{"error": "invalid key sk-live-9f8e7d"}')

    llm = http_llm(cfg, monkeypatch, handler)
    with caplog.at_level(logging.DEBUG):
        with pytest.raises(LLMError) as e:
            llm.complete("tagging", MSG, EventTags)
        logging.getLogger("ml.llm").warning("утечка? %s", "sk-oops-1")
    assert "sk-live-9f8e7d" not in str(e.value) and "sk-***" in str(e.value)
    assert "sk-oops-1" not in caplog.text and "sk-***" in caplog.text
    llm.store.log_call(run_id=None, task="t", model="m", prompt_version=None, status="error", error="boom sk-zzz999")
    dump = json.dumps(llm.store.journal())
    assert "sk-zzz999" not in dump and "boom sk-***" in dump


def test_disable_reasoning_param_only_for_listed_models(cfg, monkeypatch):
    bodies = []

    def handler(req):
        bodies.append(json.loads(req.content))
        return ok_response()

    cfg["llm"]["disable_reasoning"] = ["z-ai/glm-5.3-flash"]
    llm = http_llm(cfg, monkeypatch, handler)
    llm.call_raw("z-ai/glm-5.3-flash", MSG)
    llm.call_raw("google/gemma-4-26b-a4b-it", MSG)
    assert "reasoning" in bodies[0] and "reasoning" not in bodies[1]


def test_key_info_and_old_store_migrates(cfg, monkeypatch, tmp_path):
    llm = http_llm(cfg, monkeypatch, lambda req: httpx.Response(200, json={"data": {"remaining": 123.5}}))
    assert llm.key_info() == {"data": {"remaining": 123.5}} and MockLLM(cfg).key_info() is None
    old = tmp_path / "old.sqlite"
    with sqlite3.connect(old) as c:
        c.execute("CREATE TABLE llm_calls (id INTEGER PRIMARY KEY AUTOINCREMENT, ts TEXT NOT NULL, day TEXT NOT NULL, run_id TEXT,"
                  " task TEXT, model TEXT, prompt_version TEXT, tokens_in INTEGER, tokens_out INTEGER, duration_ms INTEGER,"
                  " status TEXT, cost_rub REAL, cached INTEGER, error TEXT)")
    st = Store(old)
    st.log_call(run_id=None, task="t", model="m", prompt_version=None, endpoint="reserve")
    assert st.journal()[0]["endpoint"] == "reserve"
