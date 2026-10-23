"""Прогон тегирования по файлу событий: таблица, итоги и out/tags.jsonl.
Использование: LLM_MODE=mock python scripts/run_tagging.py --input tests/fixtures/news_30.json [--out out/tags.jsonl] [--store PATH]"""
import _bootstrap  # noqa: F401
import argparse
import json
import sys
from pathlib import Path

if sys.stderr.encoding and sys.stderr.encoding.lower() != "utf-8":
    sys.stderr.reconfigure(encoding="utf-8")

from ml.config import get_config, load_config, resolve_path
from ml.tagging import Tagger, build_tagging_llm
from ml.tagging.tagger import PROMPT_VERSION


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--input", required=True)
    ap.add_argument("--out", default="out/tags.jsonl")
    ap.add_argument("--store", help="SQLite-файл кеша (по умолчанию paths.store из config.yaml)")
    args = ap.parse_args(argv)

    cfg = load_config(overrides={"paths": {"store": args.store}}) if args.store else get_config()
    events = json.loads(resolve_path(args.input, Path.cwd()).read_text(encoding="utf-8"))
    tagger = Tagger(build_tagging_llm(cfg), cfg)
    store = tagger.llm.store
    last_id = max((j["id"] for j in store.journal()), default=0)
    calls0 = tagger.llm.api_calls

    rows, results, type_ok, type_total = [], [], 0, 0
    for ev in events:
        res = tagger.tag_event(ev)
        results.append((ev, res))
        e = res.event
        exp = ev.get("expected_type")
        mark = "-"
        if "expected_noise" in ev:                      # грязные записи: шум, подозрительность, отсутствие Event
            type_total += 1
            good = (res.is_noise == ev["expected_noise"] and res.suspicious == ev["expected_suspicious"]
                    and (e is None) == ev["expected_event_null"])
            type_ok += good
            mark = "да" if good else "нет"
        elif exp and e is not None:
            type_total += 1
            type_ok += e.type.value == exp
            mark = "да" if e.type.value == exp else f"нет ({exp})"
        rows.append((ev["event_id"], e.type.value if e else "null", ", ".join(e.sectors) if e else "-",
                     str(e.importance) if e else "-", ("да: " + (res.noise_reason or "")) if res.is_noise else
                     (f"нет ({res.noise_reason})" if res.noise_reason else "нет"), mark))

    head = ("id", "тип", "сектора", "важность", "шум", "ожидание")
    widths = [max(len(c) for c in col) for col in zip(head, *rows)]
    for line in (head, *rows):
        print("  ".join(c.ljust(w) for c, w in zip(line, widths)).rstrip())

    journal = store.journal(last_id)
    valid = sum(1 for j in journal if j["status"] == "ok" and not j["cached"])
    invalid = sum(1 for j in journal if (j["error"] or "").startswith("невалидный JSON"))
    out = resolve_path(args.out)
    out.parent.mkdir(parents=True, exist_ok=True)
    with open(out, "w", encoding="utf-8") as f:
        for ev, res in results:
            f.write(json.dumps({"input_id": ev["event_id"], **res.model_dump(mode="json")}, ensure_ascii=False) + "\n")

    print(f"\nпромпт: {PROMPT_VERSION}; режим: {cfg['llm']['mode']}; результат: {out}")
    print(f"событий: {len(results)}; вызовов LLM: {tagger.llm.api_calls - calls0}; "
          f"валидных JSON: {100 * valid / (valid + invalid):.0f}%" if valid + invalid else
          f"событий: {len(results)}; вызовов LLM: 0 (всё из кеша или без вызова)")
    print(f"помечено шумом: {sum(r.is_noise for _, r in results)}; без Event: {sum(r.event is None for _, r in results)}; "
          f"подозрительных: {sum(r.suspicious for _, r in results)}; расход: {sum(j['cost_rub'] for j in journal):.2f} ₽")
    if type_total:
        print(f"совпало с ожидаемым (тип, либо шум/подозрительность/пустой Event): {type_ok} из {type_total}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
