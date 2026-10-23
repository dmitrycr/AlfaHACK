import json
from pathlib import Path

import pytest

from ml.company_format import (CHANNEL_TO_CS, SOURCE_TO_CS, adapt_profile, export_company, export_profile,
                      validate_company_profile)
from ml.schemas import Channel, ProfileIn

FX = Path(__file__).parent / "fixtures" / "profiles_demo.json"


@pytest.fixture(scope="module")
def demo():
    raw = json.loads(FX.read_text(encoding="utf-8"))["profiles"]
    return [ProfileIn.model_validate(p) for p in raw.values()]


def test_export_valid_for_company_service(demo):
    for p in demo:
        doc = export_profile(p, built_at="2026-10-21T00:00:00Z")
        assert validate_company_profile(doc) == []
        assert set(doc) == {"company_id", "version", "ruleset_version", "channels", "factor_exposure", "coverage",
                            "built_at", "synthetic"}
        assert all(isinstance(c["evidence"], list) and c["evidence"] for c in doc["channels"])


def test_source_mapping_and_names():
    assert SOURCE_TO_CS == {"data": "data", "matrix": "matrix", "llm": "llm"}
    assert CHANNEL_TO_CS["спрос"] == "demand" and CHANNEL_TO_CS["сырьевой"] == "commodity"


def test_affiliated_channel_skipped_and_duplicates_merged(demo):
    p = demo[0].model_copy(deep=True)
    p.channels += [Channel(name="аффилированный", weight=0.5, source="data", evidence="x"),
                   Channel(name="спрос", weight=0.2, source="matrix", evidence="a", sector="Тестовый сектор"),
                   Channel(name="спрос", weight=0.6, source="llm", evidence="b", sector="Тестовый сектор")]
    doc = export_profile(p)
    names = [(c["name"], c.get("sector")) for c in doc["channels"]]
    assert all(n != "affiliated" for n, _ in names) and len(names) == len(set(names))
    d = next(c for c in doc["channels"] if c["name"] == "demand" and c.get("sector") == "Тестовый сектор")
    assert d["weight"] == 0.6 and d["source"] == "llm"
    assert validate_company_profile(doc) == []


def test_company_document(demo):
    p = demo[0].model_copy(update={"revenue": 20_000_000_000.0,
                                   "okved": {"section": "G", "class": "46", "group": "46.5"}})
    c = export_company(p)
    assert c["revenue_minor"] == 2_000_000_000_000 and c["okved"]["group"] == "46"
    assert c["id"] == p.client_id and set(c["ved"]) == {"fts_flag", "country"}
    p2 = p.model_copy(update={"okved": {"section": "F", "class": "41", "group": "41.20"}})
    assert export_company(p2)["okved"]["group"] == "41.20"


def test_roundtrip(demo):
    for p in demo:
        back = adapt_profile(export_profile(p), export_company(p))
        assert back.client_id == p.client_id and back.sector == p.sector and back.region == p.region
        want = {(c.name, c.sector, c.source, c.weight) for c in p.channels}
        assert {(c.name, c.sector, c.source, c.weight) for c in back.channels} == want
        assert back.products == p.products


def test_adapt_ml_format_unchanged(demo):
    assert adapt_profile(demo[0].model_dump()) == demo[0]


def test_adapt_company_format_requires_company(demo):
    with pytest.raises(ValueError):
        adapt_profile(export_profile(demo[0]))


def test_adapt_company_service_example():
    prof = {"company_id": "company-metal", "version": 1, "ruleset_version": "synthetic-v1",
            "channels": [{"name": "demand", "sector": "Строительство", "weight": 0.7, "source": "matrix",
                          "evidence": ["a", "b"]}],
            "factor_exposure": {"interest_rate": None}, "coverage": None, "built_at": "2026-10-21T00:00:00Z",
            "synthetic": True}
    comp = {"id": "company-metal", "name": "Демо Металл", "okved": {"section": "C", "class": "24", "group": "24.10"},
            "sector": "Металлургия", "region": "Липецкая область", "revenue_minor": 2_000_000_000_000,
            "products": {"loan": True, "leasing": None, "deposit": False}, "ved": {"fts_flag": False}}
    p = adapt_profile(prof, comp)
    assert p.channels[0].name == "спрос" and p.channels[0].evidence == "a; b" and p.revenue == 20_000_000_000
    assert p.products["leasing"] is False and p.coverage == {}


def test_validator_rejects_bad():
    base = {"company_id": "c", "version": 1, "ruleset_version": "r", "built_at": "t", "factor_exposure": {},
            "coverage": None, "channels": [{"name": "отраслевой", "weight": 1, "source": "data", "evidence": []}]}
    assert validate_company_profile(base)


def test_export_all_seven_products(demo):
    seven = {"loan", "leasing", "guarantee", "deposit", "factoring", "rko", "zpp"}
    for p in demo:
        assert set(export_company(p)["products"]) == seven


def test_features_in_profile_top_level(demo):
    feats = {"buyers": {"sector_shares": [{"sector": "Строительство", "share": 0.34}],
                        "concentration": {"top1_share": 0.15, "top5_share": 0.6, "hhi": 0.08}}, "suppliers": None}
    p = demo[0].model_copy(update={"features": feats, "commodities": ["сталь"], "coverage": {"buyers": 0.8, "suppliers": 0.6}})
    assert "features" not in export_profile(p)                      # по умолчанию (флаг выключен) ключа нет
    doc = export_profile(p, include_features=True)
    assert doc["features"] == feats and doc["coverage"] == 0.6 and validate_company_profile(doc) == []
    assert not {"buyers", "suppliers", "concentration", "commodities"} & set(doc)
    assert "features" not in export_profile(demo[0].model_copy(update={"features": None}), include_features=True)


def test_export_script_no_extra_file_and_csv(demo, tmp_path):
    import subprocess
    import sys
    import pandas as pd
    root = Path(__file__).parents[1]
    subprocess.run([sys.executable, str(root / "scripts" / "export_profiles.py"), "--src", str(FX),
                    "--out-dir", str(tmp_path)], check=True, cwd=root)
    prof = json.loads((tmp_path / "profiles.json").read_text(encoding="utf-8"))
    assert not (tmp_path / "profiles_extra.json").exists()
    assert {d["company_id"] for d in prof} == {p.client_id for p in demo}
    assert all("аффилированный" not in json.dumps(d, ensure_ascii=False) for d in prof)
    assert len(pd.read_csv(tmp_path / "companies.csv").columns) == 13


def test_export_script_features_flag(tmp_path):
    from ml.config import get_config
    assert get_config()["export"]["include_features"] is False
    src = json.loads(FX.read_text(encoding="utf-8"))
    for v in src["profiles"].values():
        v["features"] = {"buyers": None, "suppliers": None}
    fx = tmp_path / "src.json"
    fx.write_text(json.dumps(src, ensure_ascii=False), encoding="utf-8")
    import subprocess
    import sys
    root = Path(__file__).parents[1]
    for flags, want in (([], False), (["--include-features"], True)):
        out = tmp_path / str(want)
        subprocess.run([sys.executable, str(root / "scripts" / "export_profiles.py"), "--src", str(fx), "--out-dir", str(out),
                        *flags], check=True, cwd=root)
        docs = json.loads((out / "profiles.json").read_text(encoding="utf-8"))
        assert all(("features" in d) is want for d in docs) and len(docs) == 5
