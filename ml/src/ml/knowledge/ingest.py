"""Загрузка материалов банка: текст постранично (pypdf; если пусто, pdftotext через subprocess; txt - страницы разделяет \\f),
нормализация, разбиение на фрагменты, тема по ключевым словам, bank_chunks.jsonl. Нет файлов - не падаем, а честно сообщаем."""
from __future__ import annotations

import json
import logging
import re
import shutil
import subprocess
from dataclasses import dataclass, field
from datetime import datetime, timedelta, timezone
from pathlib import Path

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
    documents: list[dict] = field(default_factory=list)       # {document, pages, chunks, published_at, date_source}
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


def extract_pages(path: Path) -> list[str]:
    """Тексты страниц (индекс 0 = страница 1). Пустой список - текста нет (например, скан без текстового слоя)."""
    if path.suffix.lower() == ".txt":
        pages = path.read_text(encoding="utf-8", errors="replace").split("\f")
    else:
        pages = []
        try:
            from pypdf import PdfReader
            pages = [(p.extract_text() or "") for p in PdfReader(str(path)).pages]
        except Exception as e:                                  # noqa: BLE001 - битый PDF не должен ронять загрузку остальных
            log.warning("%s: pypdf не смог прочитать (%s)", path.name, type(e).__name__)
        if not any(p.strip() for p in pages):
            log.info("%s: pypdf не дал текста, пробуем pdftotext", path.name)
            pages = _pdftotext(path)
    pages = [normalize(p) for p in pages]
    while pages and not pages[-1]:                              # хвостовой \f после последней страницы
        pages.pop()
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


def build_chunks(path: Path, lo: int, hi: int, overlap: int) -> tuple[list[Chunk], dict] | None:
    pages = extract_pages(path)
    if not pages:
        return None
    title = path.stem.replace("_", " ").strip()           # TODO(align): название материала = имя файла
    slug = slugify(title)
    published, source = publication_date(path, pages[0])
    chunks = []
    for page_no, page in enumerate(pages, 1):
        for n, text in enumerate(chunk_text(page, lo, hi, overlap), 1):
            chunks.append(Chunk(chunk_id=f"{slug}-p{page_no}-{n}", document=title, page=page_no, topic=pick_topic(text), text=text,
                                slug=slug, published_at=published.isoformat(), date_source=source))
    return chunks, {"document": title, "pages": len(pages), "chunks": len(chunks), "published_at": published.isoformat(),
                    "date_source": source}


def ingest_dir(src: str | Path, out: str | Path, lo: int = 500, hi: int = 800, overlap: int = 100) -> IngestReport:
    src, out = Path(src), Path(out)
    rep = IngestReport(str(src))
    files = sorted(f for f in src.iterdir() if f.suffix.lower() in SUFFIXES) if src.is_dir() else []
    if not files:
        rep.message = (f"Файлов (PDF/txt) в {src} нет: индекс не создан и не изменён. Положите материалы банка в этот каталог "
                       f"(DATA_DIR/bank_analytics) и запустите загрузку снова.")
        return rep
    all_chunks: list[Chunk] = []
    for f in files:
        built = build_chunks(f, lo, hi, overlap)
        if built is None:
            rep.skipped.append(f.name)                        # скан без текстового слоя: OCR не делаем
            continue
        all_chunks += built[0]
        rep.documents.append(built[1])
    if not all_chunks:
        rep.message = f"В {src} {len(files)} файл(ов), но текста извлечь не удалось (сканы без текстового слоя?): индекс не изменён."
        return rep
    out.parent.mkdir(parents=True, exist_ok=True)
    with open(out, "w", encoding="utf-8") as fh:
        for c in all_chunks:
            fh.write(json.dumps(c.to_json(), ensure_ascii=False) + "\n")
    rep.chunks = len(all_chunks)
    rep.message = f"Готово: {len(rep.documents)} документов, {rep.pages} страниц, {rep.chunks} фрагментов -> {out}"
    return rep
