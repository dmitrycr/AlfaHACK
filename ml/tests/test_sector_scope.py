"""enabled-флаг секторов (sectors.yaml): промпт тегирования, профили, экспорт; приведение названий секторов из ответа модели."""
import importlib.util
import json
import logging
import shutil
import sys
from pathlib import Path

import pytest
import yaml

from ml.config import CONFIG_DIR, ML_ROOT, load_config
from ml.llm import MockLLM
from ml.profile import ProfileBuilder, build_and_cache
from ml.profile.builder import split_in_scope
from ml.profile.synth import make_synthetic
from ml.refdata import load_refdata
from ml.tagging import Tagger
from ml.tagging.tagger import load_vocab

CONSTR = "Строительство и девелопмент"
WHOLESALE = "Оптовая торговля и дистрибуция"
FX = Path(__file__).parent / "fixtures"
KEY_SECTORS = {"Строительство и девелопмент", "Оптовая торговля и дистрибуция", "Транспорт и логистика", "Банки, страхование, финансы",
               "Энергетика и коммунальные услуги", "Розничная торговля и e-commerce", "Металлургия"}


def make_config(tmp_path: Path, name: str, flags: dict[str, bool], all_on: bool = True) -> str:
    d = tmp_path / name
    shutil.copytree(CONFIG_DIR, d)
    path = d / "sectors.yaml"
    data = yaml.safe_load(path.read_text(encoding="utf-8"))
    for v in data["sectors"].values():
        if all_on:
            v["enabled"] = True                                   # база: все включены (иначе - как в config/), поверх - переданные флаги
    for sector, flag in flags.items():
        data["sectors"][sector]["enabled"] = flag
    path.write_text(yaml.safe_dump(data, allow_unicode=True, sort_keys=False), encoding="utf-8")
    return str(d)


def ev(**kw) -> dict:
    base = dict(event_id="t1", title="ПИК запустил новые проекты", text="ПИК объявил о запуске новых жилых комплексов в Москве. "
                "Решение принято в сентябре.", published_at="2026-10-20T10:00:00+03:00", source="Издание",
                url="https://news.example.com/t1")
    return {**base, **kw}


def reply(**kw) -> str:
    d = dict(type="industry", sectors=[CONSTR], okved=["41"], companies=[], regions=["Москва"], factors=[],
             direction="positive", horizon="medium", importance=3, summary="Запуск проектов.", is_noise=False, noise_reason=None)
    return json.dumps({**d, **kw}, ensure_ascii=False)


@pytest.fixture(scope="module")
def synth(tmp_path_factory):
    d = tmp_path_factory.mktemp("synth_scope")
    make_synthetic(d)
    return d


def test_default_config_only_key_sectors_enabled():
    ref = load_refdata()
    assert set(ref.enabled_sector_names) == KEY_SECTORS and len(ref.sectors) == 17
    assert not any(ref.is_enabled(s) for s in ("Нефть и газ", "Прочее", "Общепит и потребительские услуги"))


# ---------- приведение названий секторов ----------
def test_model_sector_names_are_mapped_to_canonical_and_unmatched_are_logged(cfg, caplog):
    raw = reply(sectors=["Банки", "финансы", "Космос", "Машиностроение, автопром", "Энергетика"])
    t = Tagger(MockLLM(cfg, scenario=[raw]), cfg)
    with caplog.at_level(logging.INFO, logger="ml.tagging"):
        e = t.tag_event(ev(event_id="evt_x1")).event
    assert e.sectors.count("Банки, страхование, финансы") == 1 and "Энергетика и коммунальные услуги" in e.sectors   # сокращения -> каноничные
    assert "Космос" not in e.sectors and "Машиностроение, автопром, ОПК" not in e.sectors        # сектор выключен
    assert t.sector_stats["alias"] == 3 and t.sector_stats["unmatched"] == 1 and t.sector_stats["disabled"] == 1
    assert "evt_x1" in caplog.text and "Космос" in caplog.text and "не сопоставлены" in caplog.text
    notes = [j for j in t.llm.store.journal() if j["status"] == "note"]
    assert len(notes) == 1 and notes[0]["run_id"] == "tag:evt_x1" and "Космос" in notes[0]["error"]


def test_canonical_sector_lookup():
    ref = load_refdata()
    assert ref.canonical_sector("Машиностроение, автопром") == "Машиностроение, автопром, ОПК"
    assert ref.canonical_sector(" горнодобыча ") == "Горнодобыча (уголь, драгметаллы, алмазы)"
    assert ref.canonical_sector("Финансы") == "Банки, страхование, финансы"            # прежнее название
    assert ref.canonical_sector("Металлургия и добыча") is None                        # подходит двум секторам: не угадываем
    assert ref.canonical_sector("Космос") is None and ref.canonical_sector(None) is None


# ---------- enabled: промпт ----------
def test_disabling_sector_removes_it_from_prompt_and_event_enabling_returns_it(tmp_path, cfg):
    off = load_vocab(make_config(tmp_path, "off", {CONSTR: False}))
    on = load_vocab(make_config(tmp_path, "on", {CONSTR: True}))
    t_off, t_on = (Tagger(MockLLM(cfg, scenario=[reply()]), cfg, vocab=v) for v in (off, on))
    assert CONSTR not in off.sectors and CONSTR in on.sectors and len(off.sectors) == len(on.sectors) - 1
    list_off = t_off._prompt("З", "Т", []).split("Список секторов:")[1].split("\n")[0]
    list_on = t_on._prompt("З", "Т", []).split("Список секторов:")[1].split("\n")[0]
    assert CONSTR not in list_off and CONSTR in list_on
    assert CONSTR not in t_off.tag_event(ev(event_id="off1")).event.sectors   # ни из ответа модели, ни из справочника компаний (ПИК)
    assert CONSTR in t_on.tag_event(ev(event_id="on1")).event.sectors        # другой event_id: кеш тегов общий


# ---------- enabled: профили и экспорт ----------
def test_disabled_sector_clients_are_flagged_out_of_scope_and_sector_leaves_channels_and_matrix(tmp_path, synth):
    cfg = load_config()
    off = load_refdata(make_config(tmp_path, "poff", {WHOLESALE: False}))
    on = load_refdata(make_config(tmp_path, "pon", {WHOLESALE: True}))
    res_on = ProfileBuilder(synth, cfg, ref=on).build_all()
    res_off = ProfileBuilder(synth, cfg, ref=off).build_all()
    assert res_on["out_of_scope"] == {} and not res_on["profiles"]["U_5658"]["out_of_scope"]    # включение возвращает
    assert res_off["profiles"]["U_5658"]["out_of_scope"] is True                               # профиль строится, но с пометкой
    assert res_off["out_of_scope"]["U_5658"] == WHOLESALE and set(res_on["profiles"]) == set(res_off["profiles"])
    assert all(p["out_of_scope"] == (p["sector"] == WHOLESALE) for p in res_off["profiles"].values())
    assert not any(c["sector"] == WHOLESALE for p in res_off["profiles"].values() for c in p["channels"])
    assert any(c["sector"] == WHOLESALE for p in res_on["profiles"].values() for c in p["channels"])   # без выключения такие каналы были
    assert WHOLESALE not in off.demand and WHOLESALE not in off.supply and WHOLESALE in on.demand | on.supply     # матрица
    assert all(WHOLESALE not in row for row in (*off.demand.values(), *off.supply.values()))
    assert res_off["profiles"]["U_5658"]["channels"] and not [c for c in res_off["profiles"]["U_5658"]["channels"] if c["source"] == "matrix"]


def test_export_split_and_cache_follows_enabled_flags(tmp_path, synth):
    cfg = load_config()
    off = load_refdata(make_config(tmp_path, "eoff", {WHOLESALE: False}))
    on = load_refdata(make_config(tmp_path, "eon", {WHOLESALE: True}))
    out = tmp_path / "profiles.json"
    first = build_and_cache(synth, out, cfg, ref=on)
    inside, outside = split_in_scope(first["profiles"], off)             # пометка в профиле и флаги sectors.yaml учитываются при экспорте
    assert "U_5658" in outside and "U_5658" not in inside and set(inside) | set(outside) == set(first["profiles"])
    assert split_in_scope(first["profiles"], on)[1] == {}
    flagged = {c: {**p, "out_of_scope": True} for c, p in first["profiles"].items()}
    assert split_in_scope(flagged, on)[0] == {}
    assert build_and_cache(synth, out, cfg, ref=on)["from_cache"] is True
    again = build_and_cache(synth, out, cfg, ref=off)                                           # флаг изменился: пересчёт
    assert again["from_cache"] is False and "U_5658" in again["out_of_scope"]


# ---------- фикстуры: любой из двух новых секторов ----------
def test_fixture_alternatives_and_sectors_match():
    spec = importlib.util.spec_from_file_location("run_tagging_script", ML_ROOT / "scripts" / "run_tagging.py")
    sys.path.insert(0, str(ML_ROOT / "scripts"))
    try:
        mod = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(mod)
    finally:
        sys.path.remove(str(ML_ROOT / "scripts"))
    metal = ["Металлургия", "Горнодобыча (уголь, драгметаллы, алмазы)"]
    news = json.loads((FX / "news_30.json").read_text(encoding="utf-8"))
    alts = [s for n in news for s in n.get("expected_sectors", []) if isinstance(s, list)]
    assert alts and all(a == metal for a in alts)                                              # «Металлургия и добыча» = любой из двух
    flat = [s for n in news for s in n.get("expected_sectors", []) if isinstance(s, str)]
    assert "Металлургия и добыча" not in flat and all(s in load_refdata().sectors for s in flat)
    assert mod.sectors_match([metal, "Нефть и газ"], ["Горнодобыча (уголь, драгметаллы, алмазы)", "Нефть и газ"])
    assert mod.sectors_match([metal], ["Металлургия"]) and not mod.sectors_match([metal], ["Нефть и газ"])
    assert not mod.sectors_match([metal, "Нефть и газ"], ["Металлургия"])


# ---------- включение выключенного сектора одним флагом ----------
def test_enabling_oil_and_gas_with_one_flag_brings_it_into_prompt_and_event(tmp_path, cfg):
    oil = "Нефть и газ"
    shipped = load_vocab()                                                    # как в config/: нефть выключена
    flipped = load_vocab(make_config(tmp_path, "oil", {oil: True}, all_on=False))
    assert oil not in shipped.sectors and oil in flipped.sectors and len(flipped.sectors) == len(shipped.sectors) + 1
    reply_oil = reply(type="commodity", sectors=[oil, CONSTR])
    t_off = Tagger(MockLLM(cfg, scenario=[reply_oil]), cfg, vocab=shipped)
    t_on = Tagger(MockLLM(cfg, scenario=[reply_oil]), cfg, vocab=flipped)
    prompt_off, prompt_on = (t._prompt("З", "Т", []).split("Список секторов:")[1].split("\n")[0] for t in (t_off, t_on))
    assert oil not in prompt_off and oil in prompt_on
    assert t_off.tag_event(ev(event_id="oil_off")).event.sectors == [CONSTR]
    assert t_on.tag_event(ev(event_id="oil_on")).event.sectors == [oil, CONSTR]
    ref = load_refdata(make_config(tmp_path, "oil_ref", {oil: True}, all_on=False))
    assert ref.is_enabled(oil) and not load_refdata().is_enabled(oil) and oil in ref.demand | ref.supply   # строки матрицы включились
