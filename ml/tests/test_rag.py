import csv
import json
import re
import shutil
from datetime import datetime
from pathlib import Path

import pytest
from fastapi.testclient import TestClient

from ml.config import load_config
from ml.contract import SourceKind, SourceRef
from ml.knowledge import BM25Retriever, Chunk, Retriever, chunk_text, ingest_dir, tokenize
from ml.knowledge import ingest as ingest_mod
from ml.services import knowledge_app

FX = Path(__file__).parent / "fixtures"
KCFG = load_config()["knowledge"]
LO, HI = KCFG["chunk_chars"]
QUERIES = list(csv.DictReader(open(FX / "rag_queries.csv", encoding="utf-8")))
RATE, INFL, GOLD = ("Ключевая ставка (тестовый материал)", "Инфляция (тестовый материал), август 2026", "Золото (тестовый материал)")
SOURCE_ID = re.compile(r"^bank:.+#\d+$")


@pytest.fixture(scope="module")
def built(tmp_path_factory):
    out = tmp_path_factory.mktemp("rag") / "bank_chunks.jsonl"
    rep = ingest_dir(FX / "bank_analytics", out, LO, HI, KCFG["overlap"])
    return rep, out, BM25Retriever.from_file(out, KCFG["min_score"], KCFG.get("min_coverage", 0.0))


@pytest.fixture
def retriever(built):
    return built[2]


def make_pdf(pages: list[str]) -> bytes:
    n = len(pages)
    objs = [b"<< /Type /Catalog /Pages 2 0 R >>",
            f"<< /Type /Pages /Kids [{' '.join(f'{4 + 2 * i} 0 R' for i in range(n))}] /Count {n} >>".encode(),
            b"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>"]
    for i, t in enumerate(pages):
        stream = f"BT /F1 12 Tf 72 720 Td ({t}) Tj ET".encode()
        objs.append(f"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents {5 + 2 * i} 0 R "
                    f"/Resources << /Font << /F1 3 0 R >> >> >>".encode())
        objs.append(f"<< /Length {len(stream)} >>\nstream\n".encode() + stream + b"\nendstream")
    out, offs = b"%PDF-1.4\n", []
    for i, o in enumerate(objs, 1):
        offs.append(len(out))
        out += f"{i} 0 obj\n".encode() + o + b"\nendobj\n"
    xref = len(out)
    out += f"xref\n0 {len(objs) + 1}\n0000000000 65535 f \n".encode() + b"".join(f"{o:010d} 00000 n \n".encode() for o in offs)
    return out + f"trailer\n<< /Size {len(objs) + 1} /Root 1 0 R >>\nstartxref\n{xref}\n%%EOF".encode()


# ---------- ingest ----------
def test_ingest_counts_and_jsonl_fields(built):
    rep, out, r = built
    lines = [json.loads(x) for x in out.read_text(encoding="utf-8").splitlines()]
    assert len(rep.documents) == 3 and rep.pages == 6 and rep.chunks == len(lines) == len(r.chunks) == 6
    assert all({"chunk_id", "document", "page", "topic", "text"} <= set(d) for d in lines)
    assert len({d["chunk_id"] for d in lines}) == len(lines)
    assert {d["document"] for d in lines} == {RATE, INFL, GOLD}
    assert {d["page"] for d in lines} == {1, 2}
    assert {d["topic"] for d in lines if d["document"] == RATE} == {"ставка"}


def test_publication_dates_text_filename_mtime(built):
    docs = {d["document"]: d for d in built[0].documents}
    assert (docs[RATE]["date_source"], docs[RATE]["published_at"][:10]) == ("text", "2026-09-11")
    assert (docs[INFL]["date_source"], docs[INFL]["published_at"][:10]) == ("filename", "2026-08-01")
    assert docs[GOLD]["date_source"] == "mtime"
    assert all(d["published_at"].endswith("+03:00") for d in docs.values())


@pytest.mark.parametrize("text,month_year,expected", [
    ("Дата выхода: 11.09.2026", False, "2026-09-11"), ("от 5 октября 2026 года", False, "2026-10-05"),
    ("2026-07-01", False, "2026-07-01"), ("Обзор, август 2026", True, "2026-08-01"), ("Обзор, август 2026", False, None),
    ("31.02.2026", False, None), ("нет даты", True, None)])
def test_find_date(text, month_year, expected):
    d = ingest_mod.find_date(text, month_year)
    assert (d.date().isoformat() if d else None) == expected


def test_slugify_and_topics():
    assert ingest_mod.slugify("Ключевая ставка ЦБ РФ: без изменений, 14,0%") == "klyuchevaya-stavka-tsb-rf-bez-izmeneniy-14-0"
    assert ingest_mod.slugify("???") == "doc"
    pick = ingest_mod.pick_topic
    assert pick("ключевая ставка") == "ставка" and pick("рост инфляции и ИПЦ") == "инфляция" and pick("цена золота") == "золото"
    assert pick("облигации и купоны ОФЗ") == "облигации" and pick("безработица и рынок труда") == "рынок труда"
    assert pick("рост ВВП") == "ВВП" and pick("нефть и уголь") == "сырьё" and pick("потребление домохозяйств") == "потребление"
    assert pick("погода") == "прочее"


def test_no_files_is_honest_and_does_not_touch_index(tmp_path):
    out = tmp_path / "chunks.jsonl"
    for src in (tmp_path / "missing", tmp_path):
        rep = ingest_dir(src, out)
        assert rep.chunks == 0 and "нет" in rep.message and not out.exists()
    out.write_text("keep", encoding="utf-8")
    ingest_dir(tmp_path / "missing", out)
    assert out.read_text(encoding="utf-8") == "keep"
    assert BM25Retriever.from_file(tmp_path / "nope.jsonl").search("ставка") == []        # нет индекса: пусто, без падения


def test_pdf_extraction_pages(tmp_path):
    (tmp_path / "Key_rate_review.pdf").write_bytes(make_pdf(["Key rate stays at 14 percent", "Second page about gold"]))
    out = tmp_path / "c.jsonl"
    rep = ingest_dir(tmp_path, out)
    assert rep.documents[0]["document"] == "Key rate review" and rep.documents[0]["pages"] == 2
    chunks = [json.loads(x) for x in out.read_text(encoding="utf-8").splitlines()]
    assert [(c["page"], "14 percent" in c["text"]) for c in chunks] == [(1, True), (2, False)]
    assert chunks[0]["chunk_id"] == "key-rate-review-p1-1" and chunks[0]["date_source"] == "mtime"


def test_pdftotext_fallback_and_scan_skipped(tmp_path, monkeypatch):
    from pypdf import PdfWriter
    blank = tmp_path / "scan.pdf"
    w = PdfWriter()
    w.add_blank_page(200, 200)
    with open(blank, "wb") as f:
        w.write(f)
    monkeypatch.setattr(ingest_mod, "_pdftotext", lambda p: [])
    rep = ingest_dir(tmp_path, tmp_path / "c.jsonl")
    assert rep.skipped == ["scan.pdf"] and rep.chunks == 0 and "не удалось" in rep.message        # скан: пропускаем, не падаем
    monkeypatch.setattr(ingest_mod, "_pdftotext", lambda p: ["Ключевая ставка 14%.\n\nВторой абзац.", "Страница два."])
    rep = ingest_dir(tmp_path, tmp_path / "c.jsonl")
    assert rep.documents[0]["pages"] == 2 and rep.chunks == 2


def test_broken_pdf_does_not_stop_other_files(tmp_path):
    (tmp_path / "broken.pdf").write_bytes(b"not a pdf")
    (tmp_path / "ok.txt").write_text("Ключевая ставка выросла.", encoding="utf-8")
    rep = ingest_dir(tmp_path, tmp_path / "c.jsonl")
    assert [d["document"] for d in rep.documents] == ["ok"] and rep.skipped == ["broken.pdf"]


# ---------- chunker ----------
def test_chunker_sizes_overlap_and_no_loss():
    sents = [f"Предложение номер {i} про ключевую ставку и инфляцию в экономике страны." for i in range(60)]
    chunks = chunk_text(" ".join(sents), LO, HI, 100)
    assert len(chunks) > 3 and all(len(c) <= HI for c in chunks)
    assert all(len(c) >= LO for c in chunks[:-1])
    for a, b in zip(chunks, chunks[1:]):
        assert b[:40] in a and len(a) + len(b) > 0                        # перекрытие: начало следующего есть в конце предыдущего
    joined = " ".join(chunks)
    assert all(s in joined for s in sents)                                  # ничего не потеряно
    # перекрытие не длиннее заданного (по целым предложениям)
    first_words = chunks[1].split(".")[0] + "."
    assert chunks[0].endswith(first_words) and len(first_words) <= 100


def test_chunker_paragraphs_long_sentence_and_edges():
    assert chunk_text("") == [] and chunk_text("   \n\n ") == []
    assert chunk_text("Короткий текст.") == ["Короткий текст."]
    long = ("слово " * 400).strip() + "."
    parts = chunk_text(long, LO, HI, 100)
    assert len(parts) >= 3 and all(len(p) <= HI for p in parts)
    paras = chunk_text("Первый абзац. Ещё предложение.\n\nВторой абзац.", LO, HI, 100)
    assert paras == ["Первый абзац. Ещё предложение. Второй абзац."]


# ---------- tokenizer ----------
def test_tokenizer_fallback_and_stopwords():
    assert tokenize("Ключевая ставка 14%", use_morph=False)[-1] == "14"
    assert tokenize("ставки ставка ставку", use_morph=False) == ["ставк"] * 3
    assert tokenize("и в на ставка") == tokenize("ставка")
    assert tokenize("") == [] and tokenize(None) == []
    assert tokenize("ставки") == tokenize("ставка")                         # лемматизация (или запасной режим) сводит формы слова


# ---------- search ----------
def test_key_rate_query_returns_rate_document_in_top3(retriever):
    hits = retriever.search("ключевая ставка 14%", k=3)
    assert 1 <= len(hits) <= 3 and hits[0].document == RATE
    assert any("14,0%" in h.text for h in hits[:1])


def test_off_topic_query_is_empty(retriever):
    assert retriever.search("рецепт борща") == []
    assert retriever.search("прогноз погоды на выходные") == []             # одно общее слово («прогноз») - не ответ
    assert retriever.search("") == [] and retriever.search("???") == []


def test_results_have_document_page_source_id_and_valid_source_ref(retriever):
    n = 0
    for row in QUERIES:
        for h in retriever.search(row["query"], k=5):
            r = h.to_result()
            assert {"chunk_id", "document", "page", "topic", "score", "text", "source_id", "source_ref", "title", "stale"} == set(r)
            assert r["document"] and isinstance(r["page"], int) and r["page"] >= 1 and r["score"] > 0
            assert SOURCE_ID.match(r["source_id"]) and r["source_id"] == f"bank:{r['document']}#{r['page']}"
            ref = SourceRef.model_validate(r["source_ref"])                 # валидируется классом контракта
            assert ref.kind == SourceKind.bank_analytics and ref.name == r["document"]
            assert str(ref.url).startswith("https://bank.local/analytics/") and str(ref.url).endswith(h.slug)
            assert ref.fragment_id == f"{h.slug}#{r['page']}" and ref.published_at.tzinfo is not None
            n += 1
    assert n >= 20


def test_topics_filter(retriever):
    assert {h.topic for h in retriever.search("ключевая ставка", k=5, topics=["ставка"])} == {"ставка"}
    assert retriever.search("цена золота", k=5, topics=["ставка"]) == []
    got = {h.topic for h in retriever.search("цена золота", k=5, topics=["золото", "инфляция"])}
    assert "золото" in got and got <= {"золото", "инфляция"}


def test_precision_at_3_on_queries_csv(retriever, capsys):
    pos = [r for r in QUERIES if r["expected_document"]]
    neg = [r for r in QUERIES if not r["expected_document"]]
    assert len(QUERIES) >= 10
    hit = rel = 0.0
    for r in pos:
        top = retriever.search(r["query"], k=3)
        ok = [h.document == r["expected_document"] for h in top]
        hit += any(ok)
        rel += sum(ok) / 3
    precision_at_3 = hit / len(pos)                                         # доля запросов с нужным документом в топ-3
    with capsys.disabled():
        print(f"\nprecision@3 = {precision_at_3:.2f} ({int(hit)} из {len(pos)} запросов с ожидаемым документом в топ-3); "
              f"средняя доля нужных фрагментов в выдаче из 3: {rel / len(pos):.2f}; "
              f"запросов без ответа, вернувших пусто: {sum(retriever.search(r['query']) == [] for r in neg)} из {len(neg)}")
    assert precision_at_3 >= 0.7
    assert all(retriever.search(r["query"]) == [] for r in neg)


def test_retriever_interface_is_replaceable(retriever):
    class Fixed(Retriever):
        def search(self, query, k=3, topics=None):
            return [Chunk("c1", "Док", 1, "прочее", query, 1.0)]

    assert isinstance(retriever, Retriever) and Fixed().search("q")[0].source_id == "bank:Док#1"


def test_min_score_and_empty_index():
    r = BM25Retriever([Chunk("c", "Док", 1, "ставка", "ключевая ставка 14%", slug="dok", published_at="2026-09-11T00:00:00+03:00")],
                      min_score=100.0)
    assert r.search("ключевая ставка") == []
    assert BM25Retriever([]).search("ставка") == []
    assert datetime.fromisoformat(Chunk("c", "Д", 1, "t", "x", published_at="2026-09-11T00:00:00+03:00").to_json()["published_at"])


# ---------- API ----------
@pytest.fixture
def client(retriever):
    knowledge_app.set_retriever(retriever)
    yield TestClient(knowledge_app.app)
    knowledge_app.set_retriever(None)


def test_api_search_and_health(client):
    r = client.post("/search", json={"query": "ключевая ставка 14%", "k": 2})
    body = r.json()
    assert r.status_code == 200 and 1 <= len(body) <= 2 and body[0]["document"] == RATE
    assert SOURCE_ID.match(body[0]["source_id"]) and SourceRef.model_validate(body[0]["source_ref"])
    assert len(client.post("/search", json={"query": "ставка"}).json()) <= KCFG["k"]
    assert client.post("/search", json={"query": "рецепт борща"}).json() == []
    assert client.post("/search", json={"query": "цена золота", "topics": ["ставка"]}).json() == []
    assert client.post("/search", json={"query": ""}).status_code == 422
    assert client.post("/search", json={"query": "x", "k": 0}).status_code == 422
    h = client.get("/health").json()
    assert (h["status"], h["chunks"], h["documents"], h["index"]) == ("ok", 6, 3, "ready")


def test_api_empty_index_and_ingest(tmp_path, monkeypatch):
    cfg = load_config(overrides={"paths": {"data_dir": str(tmp_path), "bank_chunks": str(tmp_path / "out" / "chunks.jsonl")}})
    monkeypatch.setattr(knowledge_app, "get_config", lambda: cfg)
    knowledge_app.set_retriever(None)
    try:
        c = TestClient(knowledge_app.app)
        assert c.get("/health").json()["index"].startswith("empty")
        assert c.post("/search", json={"query": "ставка"}).json() == []
        rep = c.post("/ingest").json()
        assert rep["chunks"] == 0 and "нет" in rep["message"]                     # нет файлов: честный ответ, не ошибка
        shutil.copytree(FX / "bank_analytics", tmp_path / "bank_analytics")
        rep = c.post("/ingest").json()
        assert (rep["chunks"], rep["pages"], len(rep["documents"])) == (6, 6, 3)
        assert c.post("/search", json={"query": "ключевая ставка"}).json()[0]["document"] == RATE
    finally:
        knowledge_app.set_retriever(None)
