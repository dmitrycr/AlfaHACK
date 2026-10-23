"""Локальный OCR для сканов: страница PDF -> PNG (PyMuPDF, без poppler) -> tesseract (rus+eng). Данные никуда не отправляются.
Путь к tesseract: config paths.tesseract (по умолчанию C:\\Program Files\\Tesseract-OCR\\tesseract.exe), затем PATH.
Если tesseract не найден или нет нужных языков, поднимается OcrUnavailable с понятным сообщением (ingest останавливается)."""
from __future__ import annotations

import os
import shutil
import subprocess
import tempfile
from pathlib import Path

DEFAULT_TESSERACT = r"C:\Program Files\Tesseract-OCR\tesseract.exe"
INSTALL_HINT = ("Установите Tesseract-OCR с языками rus и eng (https://github.com/UB-Mannheim/tesseract/wiki) "
                "или укажите путь в config.yaml: paths.tesseract (переменная TESSERACT_CMD).")


class OcrUnavailable(RuntimeError):
    """Нужен OCR, а tesseract или его языковые данные не найдены."""


def find_tesseract(configured: str | None = None) -> str:
    """Сначала путь из конфига, затем PATH. Нет нигде - OcrUnavailable."""
    cfg_path = configured or DEFAULT_TESSERACT
    if Path(cfg_path).is_file():
        return cfg_path
    on_path = shutil.which("tesseract")
    if on_path:
        return on_path
    raise OcrUnavailable(f"Tesseract не найден: нет файла {cfg_path} и команды tesseract в PATH. В PDF есть страницы без текстового "
                         f"слоя (сканы), распознать их нельзя, индекс не изменён. {INSTALL_HINT}")


class TesseractOCR:
    """Движок OCR. Tesseract ищется при первом обращении (check), не при создании: ingest без сканов Tesseract не требует."""

    def __init__(self, exe: str | None = None, lang: str = "rus+eng", dpi: int = 300, workers: int = 4, timeout: int = 300):
        self.configured_exe, self.lang, self.dpi, self.workers, self.timeout = exe, lang, int(dpi), int(workers), int(timeout)
        self._exe: str | None = None

    @classmethod
    def from_config(cls, cfg: dict) -> TesseractOCR:
        o = cfg.get("knowledge", {}).get("ocr", {})
        return cls(cfg.get("paths", {}).get("tesseract") or None, o.get("lang", "rus+eng"), o.get("dpi", 300),
                   o.get("workers", 4), o.get("timeout_s", 300))

    def check(self) -> str:
        """Путь к tesseract; проверяет, что установлены нужные языки."""
        if self._exe:
            return self._exe
        exe = find_tesseract(self.configured_exe)
        try:
            out = subprocess.run([exe, "--list-langs"], capture_output=True, timeout=60, check=True)
        except (subprocess.SubprocessError, OSError) as e:
            raise OcrUnavailable(f"Tesseract {exe} не запускается ({type(e).__name__}). {INSTALL_HINT}") from None
        have = set(out.stdout.decode("utf-8", "replace").split()) | set(out.stderr.decode("utf-8", "replace").split())
        missing = [lang for lang in self.lang.split("+") if lang not in have]
        if missing:
            raise OcrUnavailable(f"У Tesseract ({exe}) нет языковых данных: {', '.join(missing)}. {INSTALL_HINT}")
        self._exe = exe
        return exe

    def ocr_page(self, pdf: Path, index: int) -> str:
        """Текст страницы index (с нуля): рендер в PNG и распознавание."""
        import pymupdf
        exe = self.check()
        with pymupdf.open(str(pdf)) as doc:
            png = doc[index].get_pixmap(dpi=self.dpi).tobytes("png")
        with tempfile.TemporaryDirectory() as tmp:
            img = Path(tmp) / "page.png"
            img.write_bytes(png)
            env = {**os.environ, "OMP_THREAD_LIMIT": "1"}            # страницы идут параллельно, потоки tesseract не нужны
            try:
                res = subprocess.run([exe, str(img), "stdout", "-l", self.lang, "--psm", "3"], capture_output=True,
                                     timeout=self.timeout, env=env, check=True)
            except subprocess.CalledProcessError as e:
                raise OcrUnavailable(f"Tesseract завершился с ошибкой на {pdf.name}, стр. {index + 1}: "
                                     f"{e.stderr.decode('utf-8', 'replace')[:200]}") from None
            except (subprocess.SubprocessError, OSError) as e:
                raise OcrUnavailable(f"Tesseract не отработал на {pdf.name}, стр. {index + 1} ({type(e).__name__})") from None
        text = res.stdout.decode("utf-8", "replace")
        return text.replace("\r\n", "\n").replace("\r", "\n")       # без \r\n: кеш на Windows не удваивает переводы строк
