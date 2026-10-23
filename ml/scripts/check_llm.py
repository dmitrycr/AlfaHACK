"""Проверка LLM-доступа: GET /models и пробный запрос «Ответь одним словом: ок» к каждой модели из конфига.
Цель: выяснить, работает ли один ключ для всех моделей. Ключ не печатается (только «задан / не задан»).
Использование: LLM_MODE=mock python scripts/check_llm.py   |   LLM_MODE=http LLM_BASE_URL=... LLM_API_KEY=... python scripts/check_llm.py"""
import _bootstrap  # noqa: F401
import os
import sys
import time

if sys.stderr.encoding and sys.stderr.encoding.lower() != "utf-8":
    sys.stderr.reconfigure(encoding="utf-8")

from ml.config import get_config
from ml.llm import LLMError, build_llm

PROMPT = [{"role": "user", "content": "Ответь одним словом: ок"}]


def models_from_config(cfg: dict) -> list[str]:
    llm = cfg["llm"]
    names = list(llm["models"].values()) + list((llm.get("fallback") or {}).values())
    return list(dict.fromkeys(names))


def main() -> int:
    cfg = get_config()
    mode = cfg["llm"]["mode"]
    print(f"режим: {mode}; LLM_BASE_URL: {os.environ.get('LLM_BASE_URL') or '-'}; "
          f"LLM_API_KEY: {'задан' if os.environ.get('LLM_API_KEY') else 'не задан'}")
    try:
        client = build_llm(cfg)
    except LLMError as e:
        print(f"ошибка: {e}")
        return 1

    listed: list[str] | None = None
    try:
        listed = client.list_models()
        print(f"GET /models: {len(listed)} моделей" if listed is not None else "GET /models: недоступен")
    except LLMError as e:
        print(f"GET /models: ошибка: {e}")

    rows = []
    for model in models_from_config(cfg):
        t0 = time.perf_counter()
        try:
            r = client.call_raw(model, PROMPT)
            res, tokens = ("ok" if r.text.strip() else "пустой ответ"), f"{r.tokens_in}/{r.tokens_out}"
        except LLMError as e:
            res, tokens = f"ошибка: {e}"[:60], "-"
        ms = int((time.perf_counter() - t0) * 1000)
        in_list = "-" if listed is None else ("да" if model in listed else "нет")
        rows.append((model, res, f"{ms}", tokens, in_list))

    head = ("модель", "результат", "задержка, мс", "токены in/out", "в /models")
    widths = [max(len(str(x)) for x in col) for col in zip(head, *rows)]
    for line in (head, *rows):
        print("  ".join(str(c).ljust(w) for c, w in zip(line, widths)).rstrip())

    ok = [r for r in rows if r[1] == "ok"]
    print(f"\nработает {len(ok)} из {len(rows)} моделей: "
          + ("один ключ подходит ко всем моделям" if len(ok) == len(rows) else "ключ подходит не ко всем моделям"))
    return 0 if len(ok) == len(rows) else 1


if __name__ == "__main__":
    sys.exit(main())
