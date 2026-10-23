"""Метаданные PDF аналитики банка и запросы для проверки поиска: сами PDF в репозитории не лежат (data/ в .gitignore)."""
import csv
from datetime import date
from pathlib import Path

import yaml

from ml.config import CONFIG_DIR

DOCS = yaml.safe_load((CONFIG_DIR / "bank_documents.yaml").read_text(encoding="utf-8"))["documents"]
QUERIES = list(csv.DictReader(open(Path(__file__).parent / "rag_queries_real.csv", encoding="utf-8", newline="")))


def test_bank_documents_yaml_has_nine_documents_with_title_date_language():
    assert len(DOCS) == 9
    for name, d in DOCS.items():
        assert d["title"].strip() and d["language"] == "ru", name
        assert isinstance(date.fromisoformat(str(d["published_at"])), date)
    assert [n for n, d in DOCS.items() if d.get("stale")] == ["ALFA_Russian_Steels_1К21PreviewUpdate_15Apr2021"]
    assert str(DOCS["Бонд_Бриф_14092026"]["published_at"]) == "2026-09-14"


NEGATIVE = list(csv.DictReader(open(Path(__file__).parent / "rag_queries_negative.csv", encoding="utf-8", newline="")))


def test_negative_queries_are_eight_distinct_russian_with_empty_expectation():
    assert len(NEGATIVE) == 8 and {q["expected"] for q in NEGATIVE} == {"empty"}
    assert len({q["query"] for q in NEGATIVE}) == 8
    assert all(any("а" <= ch.lower() <= "я" for ch in q["query"]) for q in NEGATIVE)
    real = {q["query"] for q in QUERIES}
    assert not real & {q["query"] for q in NEGATIVE}


def test_real_rag_queries_are_russian_and_point_to_known_documents():
    assert len(QUERIES) == 12
    assert all(q["expected_doc"] in DOCS for q in QUERIES)
    assert all(any("а" <= ch.lower() <= "я" for ch in q["query"]) for q in QUERIES)
    topics = {q["topic"] for q in QUERIES}
    assert topics == {"ключевая ставка", "инфляция", "потребление", "золото", "сталь", "макростатистика", "рынок облигаций"}
    assert len({q["query"] for q in QUERIES}) == 12
