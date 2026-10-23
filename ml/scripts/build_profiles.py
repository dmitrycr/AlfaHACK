"""Построение профилей для компаний с выручкой >= порога. Кеш по хешу входных файлов (PR-09).
Использование: python scripts/build_profiles.py [--data-dir DIR] [--out FILE] [--force]"""
import _bootstrap  # noqa: F401
import argparse
import json

from ml.config import get_config, resolve_path
from ml.profile import build_and_cache


def main() -> None:
    cfg = get_config()
    ap = argparse.ArgumentParser()
    ap.add_argument("--data-dir", default=cfg["paths"].get("data_dir"))
    ap.add_argument("--out", default=str(resolve_path(cfg["paths"]["profiles"])))
    ap.add_argument("--force", action="store_true")
    a = ap.parse_args()
    if not a.data_dir:
        raise SystemExit("DATA_DIR не задан (или --data-dir). Для отладки: python scripts/make_synthetic_data.py")
    res = build_and_cache(a.data_dir, a.out, cfg, force=a.force)
    print("из кеша" if res["from_cache"] else "пересчитано", f"-> {a.out}")
    print(f"профилей: {len(res['profiles'])}, ниже порога: {len(res['below_threshold'])}, "
          f"out_of_scope (сектор выключен): {len(res.get('out_of_scope', {}))}")
    print("Отчёт покрытия:")
    print(json.dumps(res["report"], ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()
