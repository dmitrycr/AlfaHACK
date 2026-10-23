"""Загрузка профилей в Company Service: GetProfile -> версия last+1 -> ReplaceProfile.
По умолчанию dry-run (читает GetProfile, ничего не записывает); запись только при apply=True.
features уходит только при export.include_features (или include_features=True)."""
from __future__ import annotations

from dataclasses import dataclass, field

import grpc

from .company_client import CompanyClient, CompanyServiceError

IGNORED_ON_COMPARE = ("version", "built_at")      # меняются при каждой сборке; на «изменился ли профиль» не влияют


@dataclass
class PushResult:
    lines: list[str] = field(default_factory=list)
    pushed: int = 0          # apply: записано; dry-run: было бы записано
    unchanged: int = 0
    failed: int = 0


def prepare(doc: dict, include_features: bool) -> dict:
    out = dict(doc)
    if not include_features:
        out.pop("features", None)
    return out


def same_profile(new: dict, stored: dict) -> bool:
    # TODO(align): пока бэкенд не хранит features, в сохранённом профиле ключа нет - его не сравниваем, иначе повторный запуск дублировал бы версии
    keys = [k for k in new if k not in IGNORED_ON_COMPARE and (k != "features" or "features" in stored)]
    return all(new.get(k) == stored.get(k) for k in keys) and not (set(stored) - set(new) - set(IGNORED_ON_COMPARE))


def push_profiles(client: CompanyClient, docs: list[dict], *, apply: bool = False, include_features: bool = False) -> PushResult:
    res = PushResult()
    verb = "записан" if apply else "был бы записан"
    for raw in docs:
        doc = prepare(raw, include_features)
        cid = doc["company_id"]
        try:
            try:
                stored = client.get_profile(cid)
            except CompanyServiceError as e:
                if e.code != grpc.StatusCode.NOT_FOUND:
                    raise
                stored = None
            if stored is not None and same_profile(doc, stored):
                res.unchanged += 1
                res.lines.append(f"{cid}: без изменений (версия {stored['version']})")
                continue
            version = (stored["version"] if stored else 0) + 1
            if apply:
                client.replace_profile(cid, doc, version)
            res.pushed += 1
            res.lines.append(f"{cid}: {verb} v{version}" + ("" if stored else " (профиля ещё нет)"))
        except CompanyServiceError as e:
            res.failed += 1
            res.lines.append(f"{cid}: ОШИБКА {e}")
    return res
