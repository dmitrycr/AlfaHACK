"""Применяет config/sector_rename.csv (old,new,note) к sector_matrix.csv и sector_commodities.csv, а с --news ещё к expected_sectors
в файле новостей. Строки с одним old и несколькими new дублируются; при слиянии секторов одинаковые (from, to, role) схлопываются
по максимальной strength, связи сектора с самим собой отбрасываются. Новые сектора (old пуст) получают 0: строк нет, TODO(align) для Андрея.
Повторный запуск ничего не меняет. Использование: python scripts/apply_sector_rename.py [--news tests/fixtures/news_30.json]"""
import _bootstrap  # noqa: F401
import argparse
import csv
import json
import sys
from pathlib import Path

if sys.stdout.encoding and sys.stdout.encoding.lower() != "utf-8":
    sys.stdout.reconfigure(encoding="utf-8")

from ml.config import CONFIG_DIR


def load_rename(path: Path) -> dict[str, list[str]]:
    table: dict[str, list[str]] = {}
    with open(path, encoding="utf-8", newline="") as f:
        for r in csv.DictReader(f):
            if r["old"]:
                table.setdefault(r["old"], []).append(r["new"])
    return table


def rename(name: str, table: dict[str, list[str]]) -> list[str]:
    return table.get(name, [name])


def write_csv(path: Path, fields: list[str], rows: list[dict]) -> None:
    with open(path, "w", encoding="utf-8", newline="") as f:
        w = csv.DictWriter(f, fieldnames=fields, lineterminator="\n")
        w.writeheader()
        w.writerows(rows)


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--config-dir", default=str(CONFIG_DIR))
    ap.add_argument("--news", help="файл новостей с expected_sectors (tests/fixtures/news_30.json)")
    a = ap.parse_args()
    d = Path(a.config_dir)
    table = load_rename(d / "sector_rename.csv")
    new_names = {n for v in table.values() for n in v}

    rows = list(csv.DictReader(open(d / "sector_matrix.csv", encoding="utf-8", newline="")))
    merged: dict[tuple, dict] = {}
    dropped = 0
    for r in rows:
        for frm in rename(r["from_sector"], table):
            for to in rename(r["to_sector"], table):
                if frm == to:
                    dropped += 1
                    continue
                key = (frm, to, r["role"])
                new = {**r, "from_sector": frm, "to_sector": to}
                if key not in merged or float(new["strength"]) > float(merged[key]["strength"]):
                    merged[key] = new
    out = list(merged.values())
    write_csv(d / "sector_matrix.csv", ["from_sector", "to_sector", "role", "strength", "rationale"], out)
    print(f"sector_matrix.csv: {len(rows)} -> {len(out)} строк (самосвязей отброшено {dropped})")

    crows = list(csv.DictReader(open(d / "sector_commodities.csv", encoding="utf-8", newline="")))
    cmerged: dict[tuple, dict] = {}
    for r in crows:
        for s in rename(r["sector"], table):
            cmerged.setdefault((s, r["commodity"], r["role"]), {**r, "sector": s})
    write_csv(d / "sector_commodities.csv", ["sector", "commodity", "role", "rationale"], list(cmerged.values()))
    print(f"sector_commodities.csv: {len(crows)} -> {len(cmerged)} строк")

    unknown = ({r["from_sector"] for r in out} | {r["to_sector"] for r in out} | {s for s, _, _ in cmerged}) - new_names
    if unknown:
        raise SystemExit(f"в справочниках остались сектора вне sector_rename.csv: {sorted(unknown)}")

    if a.news:
        p = Path(a.news)
        news = json.loads(p.read_text(encoding="utf-8"))
        for ev in news:
            if "expected_sectors" in ev:       # старое название, ставшее двумя новыми, = альтернатива: подходит любой из двух
                ev["expected_sectors"] = [s if isinstance(s, list) else
                                          (rename(s, table)[0] if len(rename(s, table)) == 1 else rename(s, table))
                                          for s in ev["expected_sectors"]]
        p.write_text(json.dumps(news, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
        print(f"{p.name}: expected_sectors переведены")
    return 0


if __name__ == "__main__":
    sys.exit(main())
