"""Собирает tests/fixtures/profiles_demo.json: 5 вымышленных демо-клиентов (НЕ реальные данные кейса).
Каналы строятся теми же правилами, что и в профиле из данных (ml.profile.builder.make_channels)."""
import _bootstrap  # noqa: F401
import json

from ml.config import ML_ROOT, get_config
from ml.profile.builder import make_channels
from ml.refdata import load_refdata, normalize_okved
from ml.schemas import ProfileIn

DEMO = [
    dict(client_id="DEMO_1", okved="46.51", region="Москва", revenue=20e9, loan=1, leasing=1, fts=False,
         buyers={"Строительство": 0.34, "Недвижимость": 0.12, "Розничная торговля": 0.05},
         suppliers={"Металлургия и добыча": 0.20, "Транспорт и логистика": 0.10},
         coverage={"buyers": 0.62, "suppliers": 0.55}, conc=None),
    dict(client_id="DEMO_2", okved="24.10", region="Липецкая область", revenue=30e9, guarantee=1, fts=True,
         country="Китай",
         buyers={"Машиностроение и промышленность": 0.25, "Строительство": 0.18},
         suppliers={"Энергетика": 0.15, "Транспорт и логистика": 0.20, "Нефть и газ": 0.08},
         coverage={"buyers": 0.50, "suppliers": 0.40},
         conc={"share": 0.45, "sector": "Машиностроение и промышленность", "region": "Челябинская область"}),
    dict(client_id="DEMO_3", okved="01.11", region="Краснодарский край", revenue=18e9, factoring=1, deposit=1, fts=True,
         country="Турция",
         buyers={"Розничная торговля": 0.30, "Оптовая торговля": 0.22},
         suppliers={"Химия и удобрения": 0.25}, coverage={"buyers": 0.52, "suppliers": 0.30}, conc=None),
    dict(client_id="DEMO_4", okved="47.11", region="Санкт-Петербург", revenue=16e9, deposit=1, fts=False,
         buyers={}, suppliers={"Агро и пищевая промышленность": 0.10}, coverage={"buyers": 0.0, "suppliers": 0.20},
         conc=None),
    dict(client_id="DEMO_5", okved="41.20", region="Москва", revenue=25e9, loan=1, leasing=1, guarantee=1, fts=False,
         buyers={"Недвижимость": 0.50}, suppliers={"Металлургия и добыча": 0.30, "Оптовая торговля": 0.20},
         coverage={"buyers": 0.50, "suppliers": 0.50}, conc=None),
]


def main() -> None:
    cfg, ref = get_config(), load_refdata()
    out = {}
    for d in DEMO:
        okv = normalize_okved(d["okved"])
        sector = ref.sector_of_class(okv["class"])
        products = {k: bool(d.get(k, 0)) for k in ("loan", "leasing", "guarantee", "deposit", "factoring")}
        products.update({"rko": True, "zpp": False})
        commodities = ref.commodities_of(sector)
        ch = make_channels(sector=sector, okved=okv, region=d["region"], products=products, fts_flag=d["fts"],
                           commodities=commodities, buyers=d["buyers"], suppliers=d["suppliers"],
                           coverage=d["coverage"], ref=ref, cfg=cfg)
        p = ProfileIn(client_id=d["client_id"], sector=sector, okved=okv, region=d["region"], revenue=d["revenue"],
                      products=products, ved={"fts_flag": d["fts"], "country": d.get("country")}, channels=ch,
                      buyers=d["buyers"], suppliers=d["suppliers"], concentration=d["conc"],
                      commodities=commodities, coverage=d["coverage"])
        out[p.client_id] = p.model_dump()
    path = ML_ROOT / "tests/fixtures/profiles_demo.json"
    path.write_text(json.dumps({"profiles": out, "_note": "вымышленные демо-профили, не данные кейса"},
                               ensure_ascii=False, indent=1), encoding="utf-8")
    print("записано:", path, list(out))


if __name__ == "__main__":
    main()
