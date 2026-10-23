"""Загрузка материалов банка в индекс: PDF/txt из DATA_DIR/bank_analytics -> bank_chunks.jsonl (paths.bank_chunks).
Страницы PDF без текстового слоя (сканы) распознаются локальным Tesseract (rus+eng), результат лежит в <src>_text/<документ>/<страница>.txt
и повторно не распознаётся. Название, дата и stale берутся из config/bank_documents.yaml.
Нет tesseract - понятное сообщение и код выхода 2 (индекс не меняется). Нет файлов - код 0, индекс не меняется.
Использование: python scripts/ingest_bank_docs.py [--src DIR] [--out FILE] [--text-dir DIR] [--force-ocr]"""
import _bootstrap  # noqa: F401
import argparse
import sys

if sys.stdout.encoding and sys.stdout.encoding.lower() != "utf-8":
    sys.stdout.reconfigure(encoding="utf-8")
if sys.stderr.encoding and sys.stderr.encoding.lower() != "utf-8":
    sys.stderr.reconfigure(encoding="utf-8")

from ml.config import CONFIG_DIR, get_config, resolve_path
from ml.knowledge import ingest_dir
from ml.knowledge.ingest import load_bank_meta
from ml.knowledge.ocr import OcrUnavailable, TesseractOCR
from ml.services.knowledge_app import bank_dir


def main(argv: list[str] | None = None) -> int:
    cfg = get_config()
    k = cfg["knowledge"]
    ap = argparse.ArgumentParser()
    ap.add_argument("--src", default=str(bank_dir(cfg)))
    ap.add_argument("--out", default=str(resolve_path(cfg["paths"]["bank_chunks"])))
    ap.add_argument("--text-dir", help="куда сохранять результат OCR (по умолчанию <src>_text)")
    ap.add_argument("--force-ocr", action="store_true", help="распознать страницы заново, игнорируя готовые .txt")
    args = ap.parse_args(argv)

    try:
        rep = ingest_dir(args.src, args.out, *k["chunk_chars"], k["overlap"], ocr=TesseractOCR.from_config(cfg),
                         text_dir=args.text_dir, meta=load_bank_meta(CONFIG_DIR / "bank_documents.yaml"), force_ocr=args.force_ocr)
    except OcrUnavailable as e:
        print(f"ОШИБКА: {e}", file=sys.stderr)
        return 2
    print(f"{'документ':48} {'стр.':>4} {'символов':>9} {'кирилл.':>8} {'чанков':>6}  OCR-страницы / короткие (<100 симв.)")
    for d in rep.documents:
        flag = " [устарел]" if d.get("stale") else ""
        print(f"{d['document'][:48]:48} {d['pages']:>4} {d['chars']:>9} {d['cyr_share']:>8.2f} {d['chunks']:>6}  "
              f"OCR {len(d['ocr_pages'])}; коротких {len(d['short_pages'])}: {d['short_pages']}; "
              f"шум: -{d['noise_dropped']} строк, -{d['pseudo_removed']} псевдослов{flag}")
    for name in rep.skipped:
        print(f"  пропущен (нет текста): {name}")
    print(rep.message)
    return 0


if __name__ == "__main__":
    sys.exit(main())
