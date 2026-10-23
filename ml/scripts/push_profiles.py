"""Загрузка profiles.json в Company Service (gRPC): GetProfile -> ReplaceProfile с версией last+1.
По умолчанию --dry-run: ничего не записывает (читает GetProfile), печатает план. Запись - только с --apply.
features отправляется только при export.include_features (config.yaml) или --include-features.
Использование: python scripts/push_profiles.py [--src export_company_service/profiles.json] [--apply] [--include-features]"""
import _bootstrap  # noqa: F401
import argparse
import json
import sys
from pathlib import Path

from ml.company_client import CompanyClient
from ml.config import get_config
from ml.push import push_profiles

MAX_LINES = 50


def main(argv: list[str] | None = None) -> int:
    cfg = get_config()
    ap = argparse.ArgumentParser()
    ap.add_argument("--src", default="export_company_service/profiles.json")
    mode = ap.add_mutually_exclusive_group()
    mode.add_argument("--dry-run", action="store_true", help="по умолчанию")
    mode.add_argument("--apply", action="store_true", help="реально отправить ReplaceProfile")
    ap.add_argument("--include-features", action="store_true", help="отправлять features (по умолчанию export.include_features)")
    ap.add_argument("--address", default=None, help="адрес gRPC (по умолчанию COMPANY_SERVICE_ADDR / конфиг)")
    a = ap.parse_args(argv)
    docs = json.loads(Path(a.src).read_text(encoding="utf-8"))
    include = a.include_features or bool(cfg.get("export", {}).get("include_features", False))
    client = CompanyClient(a.address)
    print(f"{'APPLY' if a.apply else 'DRY-RUN'}: {len(docs)} профилей -> {client.address}, features: {'да' if include else 'нет'}")
    res = push_profiles(client, docs, apply=a.apply, include_features=include)
    client.close()
    for line in res.lines[:MAX_LINES]:
        print(line)
    if len(res.lines) > MAX_LINES:
        print(f"... ещё {len(res.lines) - MAX_LINES} строк")
    print(f"итого: {'записано' if a.apply else 'будет записано'} {res.pushed}, без изменений {res.unchanged}, ошибок {res.failed}")
    return 1 if res.failed else 0


if __name__ == "__main__":
    sys.exit(main())
