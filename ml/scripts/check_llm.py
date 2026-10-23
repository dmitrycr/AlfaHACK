"""Проверка LLM-доступа: GET /key/info (остаток бюджета), GET /models и пробный запрос «Ответь одним словом: ок»
к каждой модели из конфига, затем снова /key/info: на сколько упал остаток и сколько посчитал наш клиент.
Цель: выяснить, работает ли один ключ для всех моделей, и проверить единицу тарифа (llm.price_unit_tokens).
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

PROMPT = [{"role": "user", "content": "Ответь одним словом: ок"}]


def models_from_config(cfg: dict) -> list[str]:
    llm = cfg["llm"]
    names = list(llm["models"].values()) + ([llm["card_model_final"]] if llm.get("card_model_final") else [])
    return list(dict.fromkeys(names))


def numeric_fields(obj, prefix: str = "") -> dict[str, float]:
    """Все числовые значения ответа /key/info: {"путь.к.полю": число}. TODO(align): формат ответа неизвестен."""
    out: dict[str, float] = {}
    if isinstance(obj, dict):
        for k, v in obj.items():
            out.update(numeric_fields(v, f"{prefix}{k}."))
    elif isinstance(obj, list):
        for i, v in enumerate(obj):
            out.update(numeric_fields(v, f"{prefix}{i}."))
    elif isinstance(obj, (int, float)) and not isinstance(obj, bool):
        out[prefix.rstrip(".")] = float(obj)
    return out


def read_key_info(client) -> dict[str, float] | None:
    try:
        info = client.key_info()
    except LLMError as e:
        print(f"GET /key/info: ошибка: {e}")
        return None
    if info is None:
        print("GET /key/info: недоступен")
        return None
    return numeric_fields(info)


def print_balance(title: str, fields: dict[str, float]) -> None:
    print(f"{title}: поля ответа /key/info (формат не подтверждён, TODO(align))")
    for name, val in fields.items():
        print(f"  {name} = {val:g}")


def print_balance_drop(before: dict[str, float], after: dict[str, float], our_rub: float) -> None:
    print(f"\nнаш клиент посчитал стоимость проверки: {our_rub:.6f} ₽ (price_unit_tokens по config)")
    changed = {k: before[k] - after[k] for k in before if k in after and before[k] != after[k]}
    if not changed:
        print("остаток по /key/info не изменился (возможно, учёт с задержкой): единицу тарифа проверить нельзя")
        return
    for name, drop in changed.items():
        ratio = f"; шлюз/наш = {drop / our_rub:.3g}" if our_rub else ""
        print(f"  {name}: упало на {drop:.6f}{ratio}")
    print("шлюз/наш ≈ 1: единица тарифа верна; ≈ 1000: цены за 1 тыс. токенов, а не за 1 млн (TODO(align))")


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
        print_balance("остаток бюджета до проверки", before)

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
