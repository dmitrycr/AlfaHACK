"""Загрузка материалов банка в индекс: PDF/txt из DATA_DIR/bank_analytics -> bank_chunks.jsonl (paths.bank_chunks).
Если файлов нет, честно сообщает об этом (код выхода 0, индекс не меняется).
Использование: python scripts/ingest_bank_docs.py [--src DIR] [--out FILE]"""
import _bootstrap  # noqa: F401
import argparse
import sys

if sys.stderr.encoding and sys.stderr.encoding.lower() != "utf-8":
    sys.stderr.reconfigure(encoding="utf-8")

from ml.config import get_config, resolve_path
from ml.knowledge import ingest_dir
from ml.services.knowledge_app import bank_dir


def main(argv: list[str] | None = None) -> int:
    cfg = get_config()
    k = cfg["knowledge"]
    ap = argparse.ArgumentParser()
    ap.add_argument("--src", default=str(bank_dir(cfg)))
    ap.add_argument("--out", default=str(resolve_path(cfg["paths"]["bank_chunks"])))
    args = ap.parse_args(argv)

    rep = ingest_dir(args.src, args.out, *k["chunk_chars"], k["overlap"])
    for d in rep.documents:
        print(f"  {d['document']}: страниц {d['pages']}, фрагментов {d['chunks']}, дата {d['published_at'][:10]} ({d['date_source']})")
    for name in rep.skipped:
        print(f"  пропущен (нет текстового слоя): {name}")
    print(rep.message)
    return 0


if __name__ == "__main__":
    sys.exit(main())
