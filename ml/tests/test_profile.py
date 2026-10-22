import json

import pytest

from ml.config import load_config
from ml.profile import ProfileBuilder, build_and_cache
from ml.profile.synth import make_synthetic
from ml.refdata import load_refdata, normalize_okved, section_of_class


@pytest.fixture(scope="module")
def synth(tmp_path_factory):
    d = tmp_path_factory.mktemp("synth")
    make_synthetic(d)
    return d


@pytest.fixture(scope="module")
def builder(synth):
    return ProfileBuilder(synth, load_config())


def test_okved_normalization():
    assert normalize_okved("41.2") == normalize_okved("41.20") == {"section": "F", "class": "41", "group": "41.2"}
    assert normalize_okved("46.51")["group"] == "46.5" and normalize_okved("46.51")["section"] == "G"
    assert normalize_okved("6.2")["class"] == "06" and normalize_okved(None)["class"] is None
    assert section_of_class("62") == "J" and section_of_class("24") == "C" and section_of_class("68") == "L"
    ref = load_refdata()
    assert ref.sector_of_class("46") == "Оптовая торговля" and ref.sector_of_class("96") == "Услуги и прочее"


def test_garbage_id_removed(builder):
    assert "U_428697" not in builder.d.u.index
    assert not (builder.d.pay["PAYER"].eq("U_428697") | builder.d.pay["BENEFICIARY"].eq("U_428697")).any()
    assert "U_428697" not in builder.clients_above_threshold()[0]


def test_self_payments_excluded(builder):
    assert (builder.d.pay["PAYER"] == builder.d.pay["BENEFICIARY"]).sum() == 0
    p = builder.build_profile("U_5658")
    pay = builder.d.pay
    total_in = pay.loc[pay["BENEFICIARY"] == "U_5658", "PAY_AMT"].sum()
    assert total_in < 1e8                                                   # 9 млрд самопереводов не попали
    assert sum(p.buyers.values()) + (1 - p.coverage["buyers"]) == pytest.approx(1.0, abs=1e-3)


def test_buyers_show_construction_and_real_estate(builder):
    p = builder.build_profile("U_5658")
    assert {"Строительство", "Недвижимость"} <= set(p.buyers)
    assert p.sector == "Оптовая торговля" and p.products["loan"] and p.products["leasing"]
    assert set(p.suppliers) >= {"Металлургия и добыча", "Транспорт и логистика"}
    assert 0.8 < p.coverage["buyers"] < 1.0                                  # контрагент без записи -> unknown


def test_unknown_counterparties_reduce_coverage(builder):
    p = builder.build_profile("U_25835")
    assert p.coverage["buyers"] < 0.3 and p.coverage["suppliers"] == 0.0
    assert not any(c.name in ("спрос", "предложение") and c.source == "data" for c in p.channels)
    # покрытие < 0.30 -> каналы из матрицы (у розницы покупатели в матрице не заданы, поставщики есть)
    assert any(c.name == "предложение" and c.source == "matrix" and c.sector for c in p.channels)


def test_intragroup_excluded_but_measured(builder):
    p = builder.build_profile("U_GRP1")
    assert p.coverage["intragroup_buyers"] > 0.5 and p.coverage["intragroup_suppliers"] > 0.5
    assert "Оптовая торговля" not in p.buyers                               # внутригруппового сектора в долях нет
    assert sum(p.buyers.values()) <= 1.0


def test_concentration(builder):
    p = builder.build_profile("U_4909")
    assert p.concentration["share"] > 0.4 and p.concentration["sector"] == "Машиностроение и промышленность"


def test_channels_by_rules(builder):
    p = builder.build_profile("U_5658")
    ch = {c.name: c for c in p.channels if c.name not in ("спрос", "предложение")}
    assert ch["отраслевой"].weight == 1.0 and ch["конкурент"].weight == 0.7
    assert ch["финансовый"].weight == 0.9 and ch["финансовый"].evidence
    assert ch["валютный"].weight == 0.2 and ch["региональный"].weight == 0.5 and ch["регуляторный"].weight == 0.6
    assert all(c.source in ("data", "matrix") and c.evidence for c in p.channels)
    p2 = builder.build_profile("U_4909")
    assert {c.name: c for c in p2.channels}["валютный"].weight == 0.9
    assert {c.name: c for c in p2.channels}["финансовый"].weight == 0.6        # гарантии
    assert p2.ved["country"] is None                                        # FTS_COUNTRY_NAME не используем
    assert "сталь" in p2.commodities
    p3 = builder.build_profile("U_25835")
    assert {c.name: c for c in p3.channels}["финансовый"].weight == 0.4        # только депозит


def test_threshold_and_build_all(builder):
    above, below = builder.clients_above_threshold()
    assert set(above) == {"U_4909", "U_5639", "U_5658", "U_9748", "U_GRP1", "U_25835"} and "U_LOW1" in below
    res = builder.build_all()
    assert set(res["profiles"]) == set(above) and res["report"]["clients"] == 6
    assert "share_both_cov_ge_0.5" in res["report"] and len(res["report"]["top5_concentration"]) == 5


def test_cache_recalculates_only_on_change(synth, tmp_path):
    cfg = load_config()
    out = tmp_path / "profiles.json"
    r1 = build_and_cache(synth, out, cfg)
    r2 = build_and_cache(synth, out, cfg)
    assert not r1["from_cache"] and r2["from_cache"]
    with open(synth / "df_Uid_payment_hack.csv", "a", encoding="utf-8") as f:
        f.write("U_MET0;U_5658;1000,00;1\n")
    r3 = build_and_cache(synth, out, cfg)
    assert not r3["from_cache"]
    assert json.loads(out.read_text(encoding="utf-8"))["input_hash"] == r3["input_hash"]


def test_profiles_usable_by_provider(synth, tmp_path):
    from ml.ports import FileProfileProvider
    out = tmp_path / "p.json"
    build_and_cache(synth, out, load_config())
    prov = FileProfileProvider(out)
    assert prov.get_profile("U_5658").sector == "Оптовая торговля" and "U_4909" in prov.list_clients()
