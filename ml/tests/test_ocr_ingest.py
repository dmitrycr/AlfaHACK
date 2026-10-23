"""OCR сканов в ingest и метаданные из bank_documents.yaml. Tesseract не нужен: движок OCR подменяется."""
import json
import shutil
from pathlib import Path

import pytest
from fastapi.testclient import TestClient
from pypdf import PdfWriter
from test_rag import make_pdf

from ml.config import load_config
from ml.knowledge import BM25Retriever, ingest_dir
from ml.knowledge import ocr as ocr_mod
from ml.knowledge.ingest import extract_pages, load_bank_meta
from ml.knowledge.ocr import OcrUnavailable, TesseractOCR, find_tesseract
from ml.services import knowledge_app

PAGES = ["Ключевая ставка сохранена на уровне 14 процентов. Регулятор отметил рост инфляционных ожиданий в экономике страны.",
         "Цены на золото обновили максимум на фоне покупок центральных банков. Спрос на защитные активы вырос."]


class FakeOCR:
    workers = 2

    def __init__(self, pages=PAGES):
        self.pages, self.calls, self.checked = pages, [], 0

    def check(self):
        self.checked += 1
        return "fake-tesseract"

    def ocr_page(self, pdf: Path, index: int) -> str:
        self.calls.append((pdf.name, index + 1))
        return self.pages[index].replace(". ", ".\r\n")           # как у tesseract на Windows: \r\n внутри абзаца


def scan_pdf(path: Path, n: int = 2) -> Path:
    w = PdfWriter()
    for _ in range(n):
        w.add_blank_page(200, 200)
    with open(path, "wb") as f:
        w.write(f)
    return path


def chunks_of(out: Path) -> list[dict]:
    return [json.loads(x) for x in out.read_text(encoding="utf-8").splitlines()]


# ---------- OCR сканов ----------
def test_scan_pages_are_ocred_saved_per_page_and_source_id_is_unchanged(tmp_path):
    src = tmp_path / "bank_analytics"
    src.mkdir()
    scan_pdf(src / "Gold_scan.pdf")
    out, ocr = tmp_path / "chunks.jsonl", FakeOCR()
    rep = ingest_dir(src, out, ocr=ocr)
    assert sorted(ocr.calls) == [("Gold_scan.pdf", 1), ("Gold_scan.pdf", 2)] and ocr.checked == 1
    assert (tmp_path / "bank_analytics_text" / "Gold_scan" / "1.txt").exists()                  # <src>_text/<документ>/<страница>.txt
    assert (tmp_path / "bank_analytics_text" / "Gold_scan" / "2.txt").read_text(encoding="utf-8").startswith("Цены на золото")
    ch = chunks_of(out)
    assert [(c["document"], c["page"]) for c in ch] == [("Gold scan", 1), ("Gold scan", 2)]
    r = BM25Retriever.from_file(out).chunks[1]                      # (BM25 на двух фрагментах не ранжирует: берём фрагмент напрямую)
    assert r.source_id == "bank:Gold scan#2" and "золото" in r.text.lower() and rep.documents[0]["ocr_pages"] == [1, 2]


def test_rerun_does_not_ocr_ready_pages_and_gives_identical_chunks(tmp_path):
    src = tmp_path / "bank_analytics"
    src.mkdir()
    scan_pdf(src / "Scan.pdf")
    out = tmp_path / "chunks.jsonl"
    first = FakeOCR()
    ingest_dir(src, out, ocr=first)
    before = out.read_text(encoding="utf-8")
    second = FakeOCR()
    ingest_dir(src, out, ocr=second)
    assert len(first.calls) == 2 and second.calls == [] and second.checked == 0                  # готовые страницы не распознаются
    assert out.read_text(encoding="utf-8") == before                                              # и кеш не меняет разбивку (\r\n)
    third = FakeOCR()
    ingest_dir(src, out, ocr=third, force_ocr=True)
    assert len(third.calls) == 2
    (tmp_path / "bank_analytics_text" / "Scan" / "2.txt").unlink()
    fourth = FakeOCR()
    ingest_dir(src, out, ocr=fourth)
    assert fourth.calls == [("Scan.pdf", 2)]                                                      # дораспознаётся только недостающая


def test_only_pages_without_text_layer_are_ocred(tmp_path):
    pdf = tmp_path / "Mixed.pdf"
    pdf.write_bytes(make_pdf(["Key rate stays at 14 percent", ""]))
    ocr = FakeOCR(["не используется", "Вторая страница распознана OCR про инфляцию."])
    stats: dict = {}
    pages = extract_pages(pdf, ocr, tmp_path / "t", stats=stats)
    assert ocr.calls == [("Mixed.pdf", 2)] and pages[0] == "Key rate stays at 14 percent" and "инфляцию" in pages[1]
    assert stats["ocr_pages"] == [2]


def test_without_ocr_scans_are_still_skipped_with_hint(tmp_path):
    src = tmp_path / "b"
    src.mkdir()
    scan_pdf(src / "Scan.pdf")
    rep = ingest_dir(src, tmp_path / "c.jsonl")
    assert rep.skipped == ["Scan.pdf"] and rep.chunks == 0 and "OCR не подключён" in rep.message


def test_short_ocr_pages_are_reported_as_charts_or_tables(tmp_path):
    src = tmp_path / "b"
    src.mkdir()
    scan_pdf(src / "Doc.pdf")
    rep = ingest_dir(src, tmp_path / "c.jsonl", ocr=FakeOCR([PAGES[0], "1,5 2,0 3,1"]))
    d = rep.documents[0]
    assert d["short_pages"] == [2] and d["pages"] == 2 and d["chars"] > 100 and 0.5 < d["cyr_share"] <= 1


# ---------- нет tesseract ----------
def test_missing_tesseract_stops_ingest_with_clear_message_and_keeps_index(tmp_path, monkeypatch):
    monkeypatch.setattr(ocr_mod.shutil, "which", lambda name: None)
    src = tmp_path / "b"
    src.mkdir()
    scan_pdf(src / "Scan.pdf")
    out = tmp_path / "c.jsonl"
    out.write_text("keep", encoding="utf-8")
    with pytest.raises(OcrUnavailable) as e:
        ingest_dir(src, out, ocr=TesseractOCR(str(tmp_path / "no" / "tesseract.exe")))
    assert "Tesseract не найден" in str(e.value) and "paths.tesseract" in str(e.value) and out.read_text(encoding="utf-8") == "keep"
    (src / "Text.txt").write_text("Ключевая ставка выросла.", encoding="utf-8")            # без сканов tesseract не нужен
    (src / "Scan.pdf").unlink()
    assert ingest_dir(src, out, ocr=TesseractOCR(str(tmp_path / "no" / "tesseract.exe"))).chunks == 1


def test_find_tesseract_prefers_config_then_path(tmp_path, monkeypatch):
    exe = tmp_path / "tesseract.exe"
    exe.write_text("x")
    monkeypatch.setattr(ocr_mod.shutil, "which", lambda name: "C:/on/path/tesseract.exe")
    assert find_tesseract(str(exe)) == str(exe)
    assert find_tesseract(str(tmp_path / "missing.exe")) == "C:/on/path/tesseract.exe"
    assert load_config()["paths"]["tesseract"].endswith("tesseract.exe")


def test_api_ingest_returns_503_when_tesseract_missing(tmp_path, monkeypatch):
    monkeypatch.setattr(ocr_mod.shutil, "which", lambda name: None)
    cfg = load_config(overrides={"paths": {"data_dir": str(tmp_path), "bank_chunks": str(tmp_path / "c.jsonl"),
                                           "tesseract": str(tmp_path / "none.exe")}})
    monkeypatch.setattr(knowledge_app, "get_config", lambda: cfg)
    (tmp_path / "bank_analytics").mkdir()
    scan_pdf(tmp_path / "bank_analytics" / "Scan.pdf")
    knowledge_app.set_retriever(None)
    try:
        r = TestClient(knowledge_app.app).post("/ingest")
    finally:
        knowledge_app.set_retriever(None)
    assert r.status_code == 503 and "Tesseract не найден" in r.json()["detail"]


# ---------- bank_documents.yaml ----------
def test_bank_documents_meta_goes_to_chunks_source_ref_and_stale_mark(tmp_path):
    src = tmp_path / "b"
    src.mkdir()
    (src / "Steel_2021.txt").write_text("Цены на сталь растут. Прогноз результатов за первый квартал.", encoding="utf-8")
    (src / "Fresh.txt").write_text("Ключевая ставка сохранена на уровне 14 процентов.", encoding="utf-8")
    meta = {"Steel_2021": {"title": "Российская сталь", "published_at": "2021-04-15", "language": "ru", "stale": True},
            "Fresh": {"title": "Свежий обзор", "published_at": "2026-09-11", "language": "ru"}}
    out = tmp_path / "c.jsonl"
    rep = ingest_dir(src, out, meta=meta)
    by_doc = {d["document"]: d for d in rep.documents}
    assert by_doc["Steel 2021"]["date_source"] == "config" and by_doc["Steel 2021"]["published_at"].startswith("2021-04-15")
    chunks = {c.document: c for c in BM25Retriever.from_file(out).chunks}
    r = chunks["Steel 2021"].to_result()
    assert r["source_id"] == "bank:Steel 2021#1" and r["stale"] is True and r["title"] == "Российская сталь"
    assert r["source_ref"]["name"] == "Российская сталь [устаревший материал]" and r["source_ref"]["published_at"].startswith("2021-04-15")
    fresh = chunks["Fresh"].to_result()
    assert fresh["stale"] is False and fresh["source_ref"]["name"] == "Свежий обзор" and fresh["source_ref"]["published_at"].startswith("2026-09-11")


def test_load_bank_meta_reads_real_config_and_missing_file_is_empty(tmp_path):
    from ml.config import CONFIG_DIR
    meta = load_bank_meta(CONFIG_DIR / "bank_documents.yaml")
    assert len(meta) == 9 and meta["ALFA_Russian_Steels_1К21PreviewUpdate_15Apr2021"]["stale"] is True
    assert load_bank_meta(tmp_path / "none.yaml") == {} and load_bank_meta(None) == {}


def test_fixture_documents_without_meta_keep_old_behaviour(tmp_path):
    fx = Path(__file__).parent / "fixtures" / "bank_analytics"
    src = tmp_path / "bank_analytics"
    shutil.copytree(fx, src)
    out = tmp_path / "c.jsonl"
    ingest_dir(src, out, meta={})
    ch = chunks_of(out)
    assert all(c["title"] == "" and c["stale"] is False for c in ch) and all(c["date_source"] != "config" for c in ch)
