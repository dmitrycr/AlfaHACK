"""Генерирует маленький синтетический DATA_DIR в формате реальных файлов (для отладки, НЕ реальные данные).
Использование: python scripts/make_synthetic_data.py [out_dir]"""
import _bootstrap  # noqa: F401
import sys

from ml.profile.synth import make_synthetic

if __name__ == "__main__":
    out = sys.argv[1] if len(sys.argv) > 1 else "data/synthetic"
    print(make_synthetic(out))
