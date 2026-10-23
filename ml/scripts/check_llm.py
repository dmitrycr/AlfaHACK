"""Проверка LLM-доступа: GET /key/info (остаток бюджета), GET /models и пробный запрос «Ответь одним словом: ок»
к каждой модели из конфига, затем снова /key/info: на сколько упал остаток и сколько посчитал наш клиент.
Цель: выяснить, работает ли один ключ для всех моделей, и сверить расход: рост spend у шлюза и наша оценка.
/key/info лежит в корне хоста (без /v1). Поле "key" из ответа не печатается.
Ключ не печатается (только «задан / не задан»).
Использование: LLM_MODE=mock python scripts/check_llm.py   |   LLM_MODE=http LLM_BASE_URL=... LLM_API_KEY=... python scripts/check_llm.py"""
import _bootstrap  # noqa: F401
import os
import sys
import time

if sys.stderr.encoding and sys.stderr.encoding.lower() != "utf-8":
    sys.stderr.reconfigure(encoding="utf-8")

from ml.config import get_config
from ml.llm import LLMError, build_llm
from ml.llm.base import compare_spend, parse_key_info

PROMPT = [{"role": "user", "content": "Ответь одним словом: ок"}]


def models_from_config(cfg: dict) -> list[str]:
    llm = cfg["llm"]
    names = list(llm["models"].values()) + ([llm["card_model_final"]] if llm.get("card_model_final") else [])
    return list(dict.fromkeys(names))


def read_key_info(client) -> dict | None:
    try:
        raw = client.key_info()
    except LLMError as e:
        print(f"GET /key/info: ошибка: {e}")
        return None
    if raw is None:
        print("GET /key/info: недоступен")
        return None
    info = parse_key_info(raw)
    if info is None:
        print("GET /key/info: нет info.spend / info.max_budget")
    return info


def print_balance(title: str, info: dict) -> None:
    reset = info["reset_at"]
    print(f"{title}: остаток {info['remaining']:.4f} = max_budget {info['max_budget']:g} - spend {info['spend']:.4f}; "
          f"сброс {reset:%d.%m.%Y %H:%M} МСК" if reset else
          f"{title}: остаток {info['remaining']:.4f} = max_budget {info['max_budget']:g} - spend {info['spend']:.4f}")


def print_balance_drop(before: dict, after: dict, our_rub: float) -> None:
    drop = after["spend"] - before["spend"]
    print(f"\nостаток после проверки: {after['remaining']:.4f}; spend вырос на {drop:.6f}; "
          f"наш клиент посчитал {our_rub:.6f} ₽")
    print(compare_spend(drop, our_rub))


def main() -> int:
    cfg = get_config()
    mode = cfg["llm"]["mode"]
    print(f"режим: {mode}; LLM_BASE_URL: {cfg['llm'].get('base_url') or '-'}; "
          f"LLM_BASE_URL_RESERVE: {cfg['llm'].get('base_url_reserve') or '-'}; "
          f"LLM_API_KEY: {'задан' if os.environ.get('LLM_API_KEY') else 'не задан'}")
    try:
        client = build_llm(cfg)
    except LLMError as e:
        print(f"ошибка: {e}")
        return 1

    before = read_key_info(client)
    if before is not None:
        print_balance("до проверки", before)

    listed: list[str] | None = None
    try:
        listed = client.list_models()
        print(f"GET /models: {len(listed)} моделей" if listed is not None else "GET /models: недоступен")
    except LLMError as e:
        print(f"GET /models: ошибка: {e}")

    rows = []
    our_rub = 0.0
    for model in models_from_config(cfg):
        t0 = time.perf_counter()
        try:
            r = client.call_raw(model, PROMPT)
            res, tokens = ("ok" if r.text.strip() else "пустой ответ"), f"{r.tokens_in}/{r.tokens_out}"
            our_rub += client.cost(model, r.tokens_in, r.tokens_out)
        except LLMError as e:
            res, tokens = f"ошибка: {e}"[:60], "-"
        ms = int((time.perf_counter() - t0) * 1000)
        in_list = "-" if listed is None else ("да" if model in listed else "нет")
        rows.append((model, res, f"{ms}", tokens, in_list))

    head = ("модель", "результат", "задержка, мс", "токены in/out", "в /models")
    widths = [max(len(str(x)) for x in col) for col in zip(head, *rows)]
    for line in (head, *rows):
        print("  ".join(str(c).ljust(w) for c, w in zip(line, widths)).rstrip())

    after = read_key_info(client) if before is not None else None
    if before is not None and after is not None:
        print_balance_drop(before, after, our_rub)

    ok = [r for r in rows if r[1] == "ok"]
    print(f"\nработает {len(ok)} из {len(rows)} моделей: "
          + ("один ключ подходит ко всем моделям" if len(ok) == len(rows) else "ключ подходит не ко всем моделям"))
    return 0 if len(ok) == len(rows) else 1


if __name__ == "__main__":
    sys.exit(main())
