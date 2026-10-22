"""Адаптер ML ProfileIn <-> формат Company Service (libs/go/model/model.go, contracts/openapi/backend.yaml).
Контракт ProfileIn не меняется: различия лечатся здесь. Используется из ports.py."""
from __future__ import annotations

from datetime import datetime, timezone

from .schemas import Channel, ProfileIn

# ---- Адаптер формата Company Service (libs/go/model/model.go, contracts/openapi/backend.yaml) ----
# Каналы: ML (русские имена) <-> Company Service (машинные имена, enum в ValidateProfile).
CHANNEL_TO_CS = {"отраслевой": "industry", "конкурент": "competitor", "спрос": "demand", "предложение": "supply",
                 "финансовый": "financial", "валютный": "currency", "сырьевой": "commodity",
                 "региональный": "regional", "регуляторный": "regulatory"}
# TODO(align): канала «аффилированный» в Company Service нет (enum из 9 имён); при экспорте он пропускается.
CS_TO_CHANNEL = {v: k for k, v in CHANNEL_TO_CS.items()}
# Происхождение связи: в Company Service enum совпадает с ML (data|matrix|llm = данные / экспертная матрица / модель).
SOURCE_TO_CS = {"data": "data", "matrix": "matrix", "llm": "llm"}
CS_TO_SOURCE = {v: k for k, v in SOURCE_TO_CS.items()}
RULESET_VERSION = "ml-stage1-v1"      # TODO(align): имя ruleset_version согласовать с бэкендом
MINOR_UNITS = 100                     # TODO(align): revenue_minor принят как копейки (ML revenue - рубли)


def _cs_okved(okved: dict) -> dict:
    """Company Service требует group вида NN или NN.NN с префиксом class; ML хранит «46.5» (1 знак)."""
    cls, grp = okved.get("class"), okved.get("group")
    if grp and "." in grp and len(grp.split(".")[-1]) == 1:
        grp = cls                      # TODO(align): однозначную группу не дополняем нулём (46.5 != 46.50)
    return {"section": okved.get("section"), "class": cls, "group": grp}


def export_company(p: ProfileIn, name: str | None = None) -> dict:
    """ProfileIn -> документ Company (model.Company). TODO(align): ML не знает name, по умолчанию = client_id."""
    out = {"id": p.client_id, "name": name or p.client_id, "okved": _cs_okved(p.okved), "sector": p.sector,
           "region": p.region,
           "products": {k: bool(v) for k, v in p.products.items()},   # все 7 продуктов; TODO(align): CSV-импорт Company Service знает только loan/leasing/deposit (лишние колонки отвергает)
           "ved": {"fts_flag": bool(p.ved.get("fts_flag")), "country": p.ved.get("country")}}
    if p.revenue is not None:
        out["revenue_minor"] = round(p.revenue * MINOR_UNITS)
    return out


def export_profile(p: ProfileIn, *, version: int | None = None, ruleset_version: str = RULESET_VERSION,
                   built_at: str | datetime | None = None, synthetic: bool = False) -> dict:
    """ProfileIn -> документ Profile Company Service (ReplaceProfile). version должен быть last+1 в хранилище.
    Не переносится (в Company Service нет полей, см. export_extra): buyers, suppliers, concentration, commodities."""
    ch: dict[tuple[str, str], dict] = {}
    for c in p.channels:
        name = CHANNEL_TO_CS.get(c.name)
        if name is None:
            continue
        item = {"name": name, "weight": c.weight, "source": SOURCE_TO_CS[c.source],
                "evidence": [c.evidence] if c.evidence else [f"ml:{c.name}"]}
        if c.sector:
            item["sector"] = c.sector
        key = (name, c.sector or "")
        if key not in ch or ch[key]["weight"] < c.weight:         # дубликат name:sector запрещён
            ch[key] = item
    cov = [p.coverage[k] for k in ("buyers", "suppliers") if p.coverage.get(k) is not None]
    t = built_at or datetime.now(timezone.utc)
    return {"company_id": p.client_id, "version": version or p.version, "ruleset_version": ruleset_version,
            "channels": list(ch.values()),
            "factor_exposure": {},           # TODO(align): факторы (курс, сырьё, логистика, спрос) - ключи не согласованы
            "coverage": min(cov) if cov else None,   # TODO(align): одно число; берём минимум покрытия покупателей/поставщиков
            "built_at": t if isinstance(t, str) else t.strftime("%Y-%m-%dT%H:%M:%SZ"), "synthetic": synthetic}


def export_extra(p: ProfileIn) -> dict:
    """Поля, которых нет в Company Service: доли покупателей/поставщиков, концентрация, сырьё, coverage по сторонам.
    Уходят в profiles_extra.json, в payload ReplaceProfile не попадают. TODO(align): названия полей бэкенд не назвал."""
    return {"buyers": dict(p.buyers),                  # TODO(align): сектор -> доля оборота
            "suppliers": dict(p.suppliers),            # TODO(align)
            "concentration": p.concentration,          # TODO(align): {"share","sector","region"} или null
            "commodities": list(p.commodities),        # TODO(align)
            "coverage": {"buyers": p.coverage.get("buyers"), "suppliers": p.coverage.get("suppliers")}}  # TODO(align)


def validate_company_profile(doc: dict) -> list[str]:
    """Зеркало service.ValidateProfile (Go); возвращает список нарушений."""
    err = []
    if not doc.get("company_id") or doc.get("version", 0) < 1 or not doc.get("ruleset_version") or not doc.get("built_at"):
        err.append("missing profile metadata")
    if doc.get("channels") is None or doc.get("factor_exposure") is None:
        err.append("profile collections are required")
    seen = set()
    for c in doc.get("channels") or []:
        k = (c["name"], c.get("sector", ""))
        if k in seen:
            err.append("duplicate channel")
        seen.add(k)
        if (c["name"] not in CS_TO_CHANNEL or not 0 <= c["weight"] <= 1 or c["source"] not in CS_TO_SOURCE
                or not c.get("evidence")):
            err.append(f"invalid channel {c['name']}")
    cov = doc.get("coverage")
    if cov is not None and not 0 <= cov <= 1:
        err.append("invalid coverage")
    return err


def adapt_profile(payload: dict, company: dict | None = None) -> ProfileIn:
    """Принимает формат ML (ProfileIn) или профиль Company Service (есть ключ company_id).
    Для второго нужен документ компании (company): в профиле нет сектора, ОКВЭД, региона, продуктов."""
    if "company_id" not in payload:
        return ProfileIn.model_validate(payload)
    if company is None:
        raise ValueError("профиль Company Service требует документ компании (sector, okved, region)")
    channels = [Channel(name=CS_TO_CHANNEL[c["name"]], weight=c["weight"], source=CS_TO_SOURCE[c["source"]],
                        evidence="; ".join(c.get("evidence") or []), sector=c.get("sector"))
                for c in payload["channels"]]
    ved = company.get("ved") or {}
    rev = company.get("revenue_minor")
    cov = payload.get("coverage")
    return ProfileIn(
        client_id=payload["company_id"], sector=company["sector"], okved=company["okved"], region=company["region"],
        revenue=None if rev is None else rev / MINOR_UNITS,
        products={k: bool(v) for k, v in (company.get("products") or {}).items()},   # null -> False
        ved={"fts_flag": bool(ved.get("fts_flag")), "country": ved.get("country") or None},
        channels=channels, version=payload["version"],
        # TODO(align): buyers/suppliers/concentration/commodities в Company Service не хранятся; coverage - одно число
        coverage={} if cov is None else {"buyers": cov, "suppliers": cov})
