import pytest
from pydantic import ValidationError

from ml.schemas import (Channel, ChannelContribution, Check, EventTags, Insight, InsightBlocks, ProfileIn, RankItem,
                        Statement, Trace, TraceStep)


def blocks(**kw):
    d = dict(what="a", why="b", consequences="c", actions="d", bank_view="нет данных", basis="e")
    d.update(kw)
    return InsightBlocks(**d)


def test_event_tags_valid_and_defaults():
    t = EventTags(event_id="e1", title="t", type="макро", importance=3)
    assert t.direction == "неясно" and t.horizon == "недели" and not t.is_noise


@pytest.mark.parametrize("kw", [dict(importance=0), dict(importance=6), dict(type="спорт"), dict(direction="up"),
                                dict(horizon="годы")])
def test_event_tags_invalid(kw):
    base = dict(event_id="e1", title="t", type="макро", importance=3)
    base.update(kw)
    with pytest.raises(ValidationError):
        EventTags(**base)


def test_rank_item_and_contribution():
    RankItem(client_id="c", event_id="e", score=0.5, breakdown=[ChannelContribution(
        channel="спрос", weight=1, match=0.5, contribution=0.5)], passed_level=True, reason="r")
    with pytest.raises(ValidationError):
        RankItem(client_id="c", event_id="e", score=1.5, breakdown=[], passed_level=True, reason="r")
    with pytest.raises(ValidationError):
        ChannelContribution(channel="x", weight=1.2, match=0.5, contribution=0.5)


def test_statement_layer_invalid():
    Statement(text="t", layer="fact", sources=["news:1"])
    with pytest.raises(ValidationError):
        Statement(text="t", layer="opinion")


def test_insight_and_trace_roundtrip():
    ins = Insight(insight_id="i", client_id="c", event_id="e", role="CEO", blocks=blocks(),
                  statements=[Statement(text="t", layer="ai", sources=["news:e"])])
    assert Insight.model_validate_json(ins.model_dump_json()) == ins
    assert ins.confidence == "medium" and ins.product_offer is None
    with pytest.raises(ValidationError):
        Insight(insight_id="i", client_id="c", event_id="e", role="CEO", blocks=blocks(),
                statements=[], confidence="great")
    Trace(run_id="r", steps=[TraceStep(step="s")], checks=[Check(name="n", ok=True)])
    with pytest.raises(ValidationError):
        TraceStep(step="s", status="weird")


def test_channel_and_profile():
    Channel(name="спрос", weight=0.5, source="data", sector="Строительство")
    with pytest.raises(ValidationError):
        Channel(name="неизвестный", weight=0.5, source="data")
    with pytest.raises(ValidationError):
        Channel(name="спрос", weight=1.5, source="data")
    with pytest.raises(ValidationError):
        Channel(name="спрос", weight=0.5, source="gpt")
    p = ProfileIn(client_id="c", sector="s", okved={}, region="r", products={"loan": True}, ved={}, channels=[])
    assert p.version == 1 and p.buyers == {}
    with pytest.raises(ValidationError):
        ProfileIn(client_id="c", sector="s", okved={}, region="r", ved={}, channels=[])      # нет products


def test_demo_profiles_valid(profiles):
    assert len(profiles) == 5
    assert all(p.channels for p in profiles.values())
