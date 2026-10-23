"""Контракт ML-части: теги события, результат ранжирования, карточка, трасса.
Основа: раздел 7.4 бизнес-требований v0.1. Менять поля только вместе с бэкендом (и писать в CHANGELOG.md)."""
import re
from typing import Literal, Optional

from pydantic import BaseModel, Field

EventType = Literal["регулирование", "макро", "сырьё", "корпоративное",
                    "отраслевое", "региональное", "санкции"]
Layer = Literal["fact", "bank", "ai"]


class EventTags(BaseModel):                      # F1-01: ответ Gemma на тегирование
    event_id: str
    title: str
    type: EventType
    sectors: list[str] = []
    okved_classes: list[str] = []
    companies: list[str] = []
    regions: list[str] = []
    factors: list[str] = []
    direction: Literal["+", "-", "неясно"] = "неясно"
    horizon: Literal["дни", "недели", "месяцы"] = "недели"
    importance: int = Field(ge=1, le=5)          # F1-03
    is_noise: bool = False                       # F1-05: мусор помечаем, не удаляем
    noise_reason: Optional[str] = None


class ChannelContribution(BaseModel):            # F2-02: разбивка скора
    channel: str
    weight: float = Field(ge=0, le=1)
    match: float = Field(ge=0, le=1)
    contribution: float


class RankItem(BaseModel):                       # ответ Ranking Service
    client_id: str
    event_id: str
    score: float = Field(ge=0, le=1)
    breakdown: list[ChannelContribution]
    passed_level: bool
    reason: str                                  # «показано, потому что...»


class Statement(BaseModel):                      # I-01
    text: str
    layer: Layer
    sources: list[str] = []                      # news:<id>, profile:<поле>, bank:<док>#<стр>
    profile_features: list[str] = []


class InsightBlocks(BaseModel):                  # 6 вопросов кейса; пусто = "нет данных"
    what: str
    why: str
    consequences: str
    actions: str
    bank_view: str
    basis: str


class Insight(BaseModel):
    insight_id: str
    client_id: str
    event_id: str
    role: str
    blocks: InsightBlocks
    statements: list[Statement]
    risks: list[str] = []
    opportunities: list[str] = []
    next_action: Optional[str] = None
    product_offer: Optional[str] = None
    confidence: Literal["high", "medium", "low"] = "medium"
    run_id: Optional[str] = None


class TraceStep(BaseModel):                      # T-01
    step: str
    tool: Optional[str] = None
    input_ref: Optional[str] = None
    output_ref: Optional[str] = None
    model: Optional[str] = None
    prompt_version: Optional[str] = None
    tokens_in: int = 0
    tokens_out: int = 0
    duration_ms: int = 0
    status: Literal["ok", "error", "retry"] = "ok"


class Check(BaseModel):
    name: str
    ok: bool
    detail: str = ""
    action: str = ""                             # что агент делает, если проверка не прошла


class Trace(BaseModel):
    run_id: str
    plan: list[str] = []
    steps: list[TraceStep] = []
    checks: list[Check] = []
    attempts: int = 1


# ---------- Раздел 5 ТЗ: вход от Company Service ----------
class Channel(BaseModel):
    name: Literal["отраслевой", "конкурент", "спрос", "предложение", "финансовый",
                  "валютный", "сырьевой", "региональный", "регуляторный", "аффилированный"]
    weight: float = Field(ge=0, le=1)
    source: Literal["data", "matrix", "llm"]
    evidence: str = ""
    sector: Optional[str] = None          # для «спрос» и «предложение»: сектор контрагента


class ProfileIn(BaseModel):
    client_id: str
    sector: str
    okved: dict                            # {"section","class","group"}
    region: str
    revenue: Optional[float] = None
    products: dict[str, bool]              # loan, leasing, guarantee, deposit, factoring, rko, zpp
    ved: dict                              # {"fts_flag": bool, "country": Optional[str]}
    channels: list[Channel]
    buyers: dict[str, float] = {}          # сектор -> доля оборота (0..1)
    suppliers: dict[str, float] = {}
    concentration: Optional[dict] = None   # {"share","sector","region"}
    commodities: list[str] = []
    coverage: dict = {}                    # {"buyers": 0.0-1.0, "suppliers": 0.0-1.0}
    features: Optional[dict] = None        # {"buyers": {...}|None, "suppliers": {...}|None}, см. profile/features.py
    out_of_scope: bool = False             # сектор клиента выключен (sectors.yaml, enabled: false): профиль есть, в экспорт не идёт
    version: int = 1


# ---------- A-03: самопроверка карточки (прототип validate_insight) ----------
def check_insight(card: Insight, source_texts: list[str] | None = None) -> list[Check]:
    out: list[Check] = []
    no_src = [s.text[:40] for s in card.statements if not s.sources]
    out.append(Check(name="sources", ok=not no_src, detail=str(no_src),
                     action="перегенерировать блок с требованием источников"))
    bad_layer = [s.text[:40] for s in card.statements
                 if (s.layer == "bank" and not any(x.startswith("bank:") for x in s.sources))
                 or (s.layer == "fact" and not any(x.startswith(("news:", "profile:")) for x in s.sources))]
    out.append(Check(name="layers", ok=not bad_layer, detail=str(bad_layer),
                     action="поправить слой или добавить подходящий источник"))
    feats = {f for s in card.statements for f in s.profile_features}
    out.append(Check(name="profile_features>=2", ok=len(feats) >= 2, detail=str(sorted(feats)),
                     action="повторить анализ с явным списком признаков"))
    empty = [k for k, v in card.blocks.model_dump().items() if not v.strip()]
    out.append(Check(name="blocks_filled", ok=not empty, detail=str(empty),
                     action="заполнить или написать «нет данных»"))
    words = len((card.blocks.what + " " + card.blocks.why).split())
    out.append(Check(name="collapsed<=120w", ok=words <= 120, detail=f"{words} слов",
                     action="сократить"))
    if source_texts is not None:                 # I-03: числа должны быть в источнике
        src = " ".join(source_texts)
        text = " ".join(s.text for s in card.statements)
        nums = {n.strip() for n in re.findall(r"\d+(?:[.,]\d+)?\s?%?", text)}
        missing = sorted(n for n in nums if n not in src)
        out.append(Check(name="numbers_in_sources", ok=not missing, detail=str(missing),
                         action="заменить число на число из источника или удалить"))
    return out
