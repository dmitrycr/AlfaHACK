"""Экспорт профилей ML в формат Company Service: profiles.json (список model.Profile), companies.json и companies.csv,
а также profiles_extra.json (доли, концентрация, сырьё, coverage по сторонам; ключ client_id; в Company Service не уходит).
Использование: python scripts/export_profiles.py [--src FILE] [--out-dir DIR] [--version N] [--synthetic]"""
import _bootstrap  # noqa: F401
import argparse
import json
from pathlib import Path

import pandas as pd

from ml.config import get_config, resolve_path
from ml.company_format import adapt_profile, export_company, export_extra, export_profile, validate_company_profile

CSV_COLS = ["id", "name", "okved_section", "okved_class", "okved_group", "sector", "region", "revenue_minor",
            "loan", "leasing", "deposit", "fts_flag", "fts_country"]


def main() -> None:
    cfg = get_config()
    ap = argparse.ArgumentParser()
    ap.add_argument("--src", default=str(resolve_path(cfg["paths"]["profiles"])))
    ap.add_argument("--out-dir", default="export_company_service")
    ap.add_argument("--version", type=int, default=None, help="версия профиля (по умолчанию ProfileIn.version)")
    ap.add_argument("--synthetic", action="store_true")
    a = ap.parse_args()
    raw = json.loads(Path(a.src).read_text(encoding="utf-8"))
    profiles = [adapt_profile(p) for cid, p in raw.get("profiles", raw).items() if not cid.startswith("_")]
    docs = [export_profile(p, version=a.version, synthetic=a.synthetic) for p in profiles]
    companies = [export_company(p) for p in profiles]
    bad = {d["company_id"]: e for d in docs if (e := validate_company_profile(d))}
    if bad:
        raise SystemExit(f"профили не проходят проверку Company Service: {bad}")
    out = Path(a.out_dir)
    out.mkdir(parents=True, exist_ok=True)
    (out / "profiles.json").write_text(json.dumps(docs, ensure_ascii=False, indent=2), encoding="utf-8")
    (out / "companies.json").write_text(json.dumps(companies, ensure_ascii=False, indent=2), encoding="utf-8")
    (out / "profiles_extra.json").write_text(
        json.dumps({p.client_id: export_extra(p) for p in profiles}, ensure_ascii=False, indent=2), encoding="utf-8")
    rows = [{"id": c["id"], "name": c["name"], "okved_section": c["okved"]["section"],
             "okved_class": c["okved"]["class"], "okved_group": c["okved"]["group"], "sector": c["sector"],
             "region": c["region"], "revenue_minor": c.get("revenue_minor", ""),
             **{k: str(c["products"][k]).lower() if k in c["products"] else "" for k in ("loan", "leasing", "deposit")},
             "fts_flag": str(c["ved"]["fts_flag"]).lower(), "fts_country": c["ved"].get("country") or ""}
            for c in companies]
    # CSV-импорт Company Service принимает только эти 13 колонок (остальные продукты - в companies.json)
    pd.DataFrame(rows, columns=CSV_COLS).to_csv(out / "companies.csv", index=False, encoding="utf-8")
    print(f"профилей: {len(docs)} -> {out}")


if __name__ == "__main__":
    main()
