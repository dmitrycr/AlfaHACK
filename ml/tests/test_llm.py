import json
import logging

import httpx
import pytest

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
    cfg["llm"]["prices"] = {"gemma-4-26b-a4b": {"in": 100.0, "out": 100.0}}     # ₽ за 1000 токенов
    cfg["llm"]["daily_limit_rub"] = 1
    llm = MockLLM(cfg)
    llm.complete("tagging", MSG, EventTags)
    assert llm.store.journal()[0]["cost_rub"] > 0 and llm.budget.state() == "exceeded"


def test_no_prices_cost_zero_and_todo_warning(cfg, caplog):
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
    seen = []

    def handler(req: httpx.Request) -> httpx.Response:
        seen.append(req)
        if req.method == "GET":
            return httpx.Response(200, json={"data": [{"id": "gemma-4-26b-a4b"}]})
        body = json.loads(req.content)
        if len(seen) == 2:
            return httpx.Response(503)                              # сбой -> повтор
        assert body["model"] == "gemma-4-26b-a4b" and body["messages"] == MSG
        content = json.dumps({"event_id": "e", "title": "t", "type": "макро", "importance": 3})
        return httpx.Response(200, json={"choices": [{"message": {"content": content}}],
                                         "usage": {"prompt_tokens": 7, "completion_tokens": 5}})

    llm = HttpLLM(cfg, client=httpx.Client(transport=httpx.MockTransport(handler)))
    assert llm.list_models() == ["gemma-4-26b-a4b"]
    with caplog.at_level(logging.DEBUG):
        res = llm.complete("tagging", MSG, EventTags)
    assert res.importance == 3 and len(seen) == 3
    assert seen[1].url.path == "/v1/chat/completions" and seen[1].headers["authorization"] == "Bearer sk-secret-123"
    j = llm.store.journal()
    assert [r["status"] for r in j] == ["retry", "ok"] and (j[1]["tokens_in"], j[1]["tokens_out"]) == (7, 5)
    assert "sk-secret-123" not in caplog.text and "sk-secret-123" not in json.dumps(j)
