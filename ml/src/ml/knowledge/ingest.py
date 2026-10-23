"""Загрузка материалов банка: текст постранично (pypdf; если пусто, pdftotext через subprocess; txt - страницы разделяет \\f),
нормализация, разбиение на фрагменты, тема по ключевым словам, bank_chunks.jsonl. Нет файлов - не падаем, а честно сообщаем."""
from __future__ import annotations

import json
import logging
import re
import shutil
import subprocess
from concurrent.futures import ThreadPoolExecutor
from dataclasses import dataclass, field
from datetime import datetime, timedelta, timezone
from pathlib import Path

import yaml

from .chunker import chunk_text
from .retriever import Chunk

log = logging.getLogger("ml.knowledge")

MSK = timezone(timedelta(hours=3))
TOPICS = ["ставка", "инфляция", "золото", "сырьё", "облигации", "потребление", "рынок труда", "ВВП"]
_TOPIC_RE = {
    "ставка": r"ставк|ключев",
    "инфляция": r"инфляц|\bипц\b|рост\w* цен|потребительск\w+ цен",
    "золото": r"золот",
    "сырьё": r"нефт|brent|commodit|сырь|металл|медь|уголь|удобрен|пшениц|\bгаз",
    "облигации": r"облигац|\bбонд|\bофз\b|купон",
    "потребление": r"потреблен|розничн|ритейл",
    "рынок труда": r"безработиц|рынок труда|рынка труда|занятост|зарплат",
    "ВВП": r"\bввп\b|экономическ\w+ рост|рост\w* экономики",
}
_MONTHS = {m: i for i, m in enumerate("января февраля марта апреля мая июня июля августа сентября октября ноября декабря".split(), 1)}
_MONTHS_NOM = {m: i for i, m in enumerate("январь февраль март апрель май июнь июль август сентябрь октябрь ноябрь декабрь".split(), 1)}
_TRANSLIT = dict(zip("абвгдеёжзийклмнопрстуфхцчшщъыьэюя",
                     ["a", "b", "v", "g", "d", "e", "e", "zh", "z", "i", "y", "k", "l", "m", "n", "o", "p", "r", "s", "t", "u",
                      "f", "kh", "ts", "ch", "sh", "shch", "", "y", "", "e", "yu", "ya"]))
SUFFIXES = (".pdf", ".txt")


@dataclass
class IngestReport:
    src_dir: str
    documents: list[dict] = field(default_factory=list)       # {document, pages, chunks, published_at, date_source, chars, cyr_share, ocr_pages, short_pages}
    skipped: list[str] = field(default_factory=list)
    chunks: int = 0
    message: str = ""

    @property
    def pages(self) -> int:
        return sum(d["pages"] for d in self.documents)


def slugify(name: str) -> str:
    s = "".join(_TRANSLIT.get(ch, ch) for ch in name.lower())
    return re.sub(r"[^a-z0-9]+", "-", s).strip("-") or "doc"


def normalize(text: str) -> str:
    """Пробелы схлопываются, абзацы (пустые строки) сохраняются, переносы строк внутри абзаца склеиваются."""
    text = text.replace("\r", "").replace("\xa0", " ")
    paras = [" ".join(p.split()) for p in re.split(r"\n\s*\n", text)]
    return "\n\n".join(p for p in paras if p)


def pick_topic(text: str) -> str:
    low = text.lower()
    counts = {t: len(re.findall(p, low)) for t, p in _TOPIC_RE.items()}
    best = max(counts.values())
    return next(t for t in TOPICS if counts[t] == best) if best else "прочее"


def _pdftotext(path: Path) -> list[str]:
    exe = shutil.which("pdftotext")
    if not exe:
        return []
    try:
        out = subprocess.run([exe, "-layout", str(path), "-"], capture_output=True, timeout=120, check=True).stdout
    except (subprocess.SubprocessError, OSError):
        return []
    return out.decode("utf-8", "replace").split("\f")


SHORT_PAGE_CHARS = 100          # страница короче: скорее всего график или таблица, OCR почти ничего не дал


def load_bank_meta(path: str | Path | None) -> dict[str, dict]:
    """config/bank_documents.yaml -> {имя файла без расширения: {title, published_at, language, stale}}. Нет файла - пусто."""
    p = Path(path) if path else None
    if not p or not p.exists():
        return {}
    return (yaml.safe_load(p.read_text(encoding="utf-8")) or {}).get("documents", {}) or {}


def _ocr_pages(path: Path, empty: list[int], ocr, text_dir: Path | None, force: bool) -> dict[int, str]:
    """OCR страниц без текстового слоя. Готовые страницы (text_dir/<документ>/<страница>.txt) повторно не распознаются."""
    doc_dir = text_dir / path.stem if text_dir else None
    got: dict[int, str] = {}
    todo = []
    for i in empty:
        f = doc_dir / f"{i + 1}.txt" if doc_dir else None
        if f is not None and f.exists() and not force:
            got[i] = f.read_text(encoding="utf-8")
        else:
            todo.append(i)
    if todo:
        ocr.check()                                             # нет tesseract: остановиться сразу, до долгой работы
        log.info("%s: OCR %d стр. (из кеша %d)", path.name, len(todo), len(got))

        def one(i: int) -> tuple[int, str]:
            return i, ocr.ocr_page(path, i)

        with ThreadPoolExecutor(max_workers=max(1, int(getattr(ocr, "workers", 1)))) as pool:
            for i, text in pool.map(one, todo):
                got[i] = text
                if doc_dir is not None:
                    doc_dir.mkdir(parents=True, exist_ok=True)
                    with open(doc_dir / f"{i + 1}.txt", "w", encoding="utf-8", newline="\n") as fh:
                        fh.write(text)
    return got


def extract_pages(path: Path, ocr=None, text_dir: Path | None = None, force_ocr: bool = False, stats: dict | None = None) -> list[str]:
    """Тексты страниц (индекс 0 = страница 1). Пустой список - текста нет (например, скан без текстового слоя, а OCR не задан).
    ocr: движок с check() и ocr_page(pdf, индекс); страницы без текстового слоя распознаются и кешируются в text_dir.
    stats (если передан) получает ocr_pages (номера страниц с 1) и short_pages (страницы короче SHORT_PAGE_CHARS)."""
    n_pdf = None
    if path.suffix.lower() == ".txt":
        pages = path.read_text(encoding="utf-8", errors="replace").split("\f")
    else:
        pages = []
        try:
            from pypdf import PdfReader
            reader = PdfReader(str(path))
            n_pdf = len(reader.pages)
            pages = [(p.extract_text() or "") for p in reader.pages]
        except Exception as e:                                  # noqa: BLE001 - битый PDF не должен ронять загрузку остальных
            log.warning("%s: pypdf не смог прочитать (%s)", path.name, type(e).__name__)
        if not any(p.strip() for p in pages):
            log.info("%s: pypdf не дал текста, пробуем pdftotext", path.name)
            pages = _pdftotext(path)
            if not any(p.strip() for p in pages) and n_pdf:
                pages = [""] * n_pdf                            # скан: страницы есть, текста нет
    pages = [normalize(p) for p in pages]
    ocr_done: list[int] = []
    if ocr is not None and n_pdf is not None and path.suffix.lower() == ".pdf":
        empty = [i for i, p in enumerate(pages) if not p]
        if empty:
            got = _ocr_pages(path, empty, ocr, text_dir, force_ocr)
            for i, text in got.items():
                pages[i] = normalize(text)
            ocr_done = [i + 1 for i in sorted(got)]
    while pages and not pages[-1]:                              # хвостовой \f после последней страницы
        pages.pop()
    if stats is not None:
        stats["ocr_pages"] = [p for p in ocr_done if p <= len(pages)]
        stats["short_pages"] = [i for i, p in enumerate(pages, 1) if len(p) < SHORT_PAGE_CHARS]
    return pages if any(pages) else []


def _date(m: re.Match | None, order: str) -> datetime | None:
    if not m:
        return None
    try:
        if order == "dmy":
            return datetime(int(m[3]), int(m[2]), int(m[1]), tzinfo=MSK)
        if order == "d-month-y":
            return datetime(int(m[3]), _MONTHS[m[2].lower()], int(m[1]), tzinfo=MSK)
        if order == "ymd":
            return datetime(int(m[1]), int(m[2]), int(m[3]), tzinfo=MSK)
        return datetime(int(m[2]), _MONTHS_NOM[m[1].lower()], 1, tzinfo=MSK)           # «август 2026» -> 1 августа
    except (ValueError, KeyError):
        return None


_DATE_FORMS = [
    (re.compile(r"(?<!\d)(\d{1,2})\.(\d{1,2})\.(\d{4})(?!\d)"), "dmy"),
    (re.compile(r"(?<!\d)(\d{1,2})\s+(" + "|".join(_MONTHS) + r")\s+(\d{4})", re.I), "d-month-y"),
    (re.compile(r"(?<!\d)(\d{4})-(\d{2})-(\d{2})(?!\d)"), "ymd"),
    (re.compile(r"\b(" + "|".join(_MONTHS_NOM) + r")\s+(\d{4})", re.I), "month-y"),
]


def find_date(text: str, month_year: bool) -> datetime | None:
    for pat, order in _DATE_FORMS:
        if order == "month-y" and not month_year:
            continue
        d = _date(pat.search(text), order)
        if d:
            return d
    return None


def publication_date(path: Path, first_page: str) -> tuple[datetime, str]:
    """Дата из текста (первая полная дата начала первой страницы), затем из имени файла, иначе дата изменения файла.
    TODO(align): подтвердить у команды, откуда брать дату выхода материала."""
    d = find_date(first_page[:2000], month_year=False)
    if d:
        return d, "text"
    d = find_date(path.stem, month_year=True)
    if d:
        return d, "filename"
    return datetime.fromtimestamp(path.stat().st_mtime, MSK), "mtime"


def _config_date(value) -> datetime | None:
    try:
        d = datetime.fromisoformat(str(value))
    except ValueError:
        return None
    return d if d.tzinfo else d.replace(tzinfo=MSK)


def build_chunks(path: Path, lo: int, hi: int, overlap: int, ocr=None, text_dir: Path | None = None, meta: dict | None = None,
                 force_ocr: bool = False) -> tuple[list[Chunk], dict] | None:
    stats: dict = {}
    pages = extract_pages(path, ocr, text_dir, force_ocr, stats)
    if not pages:
        return None
    document = path.stem.replace("_", " ").strip()        # идентификатор документа: source_id = bank:<document>#<страница>
    slug = slugify(document)
    info = (meta or {}).get(path.stem, {})
    published, source = publication_date(path, pages[0])
    if info.get("published_at") and (cfg_date := _config_date(info["published_at"])):
        published, source = cfg_date, "config"
    title, stale, language = str(info.get("title") or ""), bool(info.get("stale", False)), str(info.get("language") or "")
    chunks = []
    for page_no, page in enumerate(pages, 1):
        for n, text in enumerate(chunk_text(page, lo, hi, overlap), 1):
            chunks.append(Chunk(chunk_id=f"{slug}-p{page_no}-{n}", document=document, page=page_no, topic=pick_topic(text), text=text,
                                slug=slug, published_at=published.isoformat(), date_source=source, title=title, stale=stale,
                                language=language))
    chars = sum(len(p) for p in pages)
    cyr, lat = (sum(len(re.findall(rx, p)) for p in pages) for rx in (r"[а-яёА-ЯЁ]", r"[a-zA-Z]"))
    return chunks, {"document": document, "pages": len(pages), "chunks": len(chunks), "published_at": published.isoformat(),
                    "date_source": source, "chars": chars, "cyr_share": round(cyr / (cyr + lat), 3) if cyr + lat else 0.0,
                    "ocr_pages": stats["ocr_pages"], "short_pages": stats["short_pages"], "stale": stale}


def ingest_dir(src: str | Path, out: str | Path, lo: int = 500, hi: int = 800, overlap: int = 100, *, ocr=None,
               text_dir: str | Path | None = None, meta: dict | None = None, force_ocr: bool = False) -> IngestReport:
    """ocr: движок (см. ocr.TesseractOCR) для страниц без текстового слоя; не задан - сканы пропускаются.
    text_dir: куда сохранять результат OCR (<text_dir>/<документ>/<страница>.txt); по умолчанию рядом с src: <src>_text.
    meta: {имя файла без расширения: {title, published_at, language, stale}} (load_bank_meta). Нет tesseract - OcrUnavailable."""
    src, out = Path(src), Path(out)
    text_dir = Path(text_dir) if text_dir else src.with_name(src.name + "_text")
    rep = IngestReport(str(src))
    files = sorted(f for f in src.iterdir() if f.suffix.lower() in SUFFIXES) if src.is_dir() else []
    if not files:
        rep.message = (f"Файлов (PDF/txt) в {src} нет: индекс не создан и не изменён. Положите материалы банка в этот каталог "
                       f"(DATA_DIR/bank_analytics) и запустите загрузку снова.")
        return rep
    all_chunks: list[Chunk] = []
    for f in files:
        built = build_chunks(f, lo, hi, overlap, ocr, text_dir, meta, force_ocr)
        if built is None:
            rep.skipped.append(f.name)                        # скан без текстового слоя: OCR не делаем
            continue
        all_chunks += built[0]
        rep.documents.append(built[1])
    if not all_chunks:
        rep.message = (f"В {src} {len(files)} файл(ов), но текста извлечь не удалось (сканы без текстового слоя?): индекс не изменён."
                       + ("" if ocr is not None else " OCR не подключён."))
        return rep
    out.parent.mkdir(parents=True, exist_ok=True)
    with open(out, "w", encoding="utf-8") as fh:
        for c in all_chunks:
            fh.write(json.dumps(c.to_json(), ensure_ascii=False) + "\n")
    rep.chunks = len(all_chunks)
    rep.message = f"Готово: {len(rep.documents)} документов, {rep.pages} страниц, {rep.chunks} фрагментов -> {out}"
    return rep
