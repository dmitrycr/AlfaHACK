"""
Контракт данных MVP «Новостная аналитика для крупного бизнеса» (Альфа Хак).
Версия 0.1, черновик к КТ0. Основа: раздел 7.4 БТ.

Как пользоваться:
  python schemas.py            -> проверяет примеры и сохраняет
                                  contract.schema.json (для фронта)
                                  mock_examples.json  (моки для фронта и тестов)

Порядок данных в системе:
  Event (уровни 0 и I) -> Match (уровень II, скор под клиента)
  -> Insight (уровень III, карточка) ; Trace (журнал агента)
  Profile строится заранее и кешируется (PR-09).
"""

from __future__ import annotations

import json
from datetime import datetime
from enum import Enum
from typing import Literal, Optional

from pydantic import BaseModel, ConfigDict, Field, HttpUrl, model_validator

SCHEMA_VERSION = "0.1"


class Base(BaseModel):
    # Лишние поля запрещены: опечатка в имени поля сразу даст ошибку,
    # а не тихо потеряется по дороге между командами.
    model_config = ConfigDict(extra="forbid", populate_by_name=True)


# ---------- Справочники (фиксированные списки значений) ----------

class EventType(str, Enum):
    regulation = "regulation"      # регулирование, налоги, требования
    macro = "macro"                # ставка, инфляция, курс, макростатистика
    commodity = "commodity"        # цены на сырьё
    corporate = "corporate"        # события конкретных компаний
    industry = "industry"          # отраслевые события
    regional = "regional"          # события в регионе
    sanctions = "sanctions"        # санкции, пошлины, ограничения


class Direction(str, Enum):
    positive = "positive"
    negative = "negative"
    unclear = "unclear"


class Horizon(str, Enum):
    short = "short"     # до месяца
    medium = "medium"   # 1-6 месяцев
    long = "long"       # больше полугода


class Channel(str, Enum):
    """Каналы влияния из раздела 5.2 БТ. «Аффилированный» убран (PR-08 вне MVP)."""
    direct_industry = "direct_industry"  # своя отрасль
    competitor = "competitor"            # игрок того же сектора
    demand = "demand"                    # покупатели клиента
    supply = "supply"                    # поставщики клиента
    financial = "financial"              # стоимость и доступность денег
    fx_ved = "fx_ved"                    # курс, санкции, пошлины при ВЭД
    commodity = "commodity"              # сырьё, которое клиент делает или покупает
    regional = "regional"                # регион присутствия
    regulatory = "regulatory"            # налоги, отчётность, требования


class ChannelSource(str, Enum):
    """Откуда взят вес канала (PR-02)."""
    data = "data"        # граф платежей, факт
    matrix = "matrix"    # межотраслевая матрица, экспертная норма
    llm = "llm"          # гипотеза модели, низкий вес


class Role(str, Enum):
    owner = "owner"
    ceo = "ceo"
    cfo = "cfo"
    treasury = "treasury"
    chief_accountant = "chief_accountant"
    other_c_level = "other_c_level"


class Layer(str, Enum):
    """Три слоя достоверности (раздел 7.2, I-01)."""
    fact = "fact"   # подтверждено источником
    bank = "bank"   # позиция Альфа-Банка
    ai = "ai"       # вывод модели


class SourceKind(str, Enum):
    news = "news"
    bank_analytics = "bank_analytics"


# ---------- Источник ----------

class SourceRef(Base):
    url: HttpUrl
    name: str = Field(description="Издание или название материала банка")
    published_at: datetime
    kind: SourceKind = SourceKind.news
    fragment_id: Optional[str] = Field(
        default=None, description="Фрагмент материала банка в RAG (N-03)")


# ---------- Event: событие после уровней 0 и I ----------

class Event(Base):
    event_id: str
    title: str
    summary: str = Field(description="2-3 предложения, что произошло")
    sources: list[SourceRef] = Field(min_length=1, description="N-02: без URL события нет")
    type: EventType
    sectors: list[str] = Field(default_factory=list, description="Сектора из players_by_sector")
    okved: list[str] = Field(default_factory=list, description="ОКВЭД-классы, 2 знака")
    companies: list[str] = Field(default_factory=list)
    regions: list[str] = Field(default_factory=list)
    factors: list[str] = Field(default_factory=list, description="key_rate, usd, cny, steel...")
    direction: Direction
    horizon: Horizon
    importance: int = Field(ge=1, le=5, description="F1-03, по явным правилам")
    collected_at: datetime


# ---------- Profile: профиль компании (раздел 5) ----------

class Okved(Base):
    section: str = Field(description="Раздел, буква: C")
    class_: str = Field(alias="class", description="Класс, 2 знака: 24")
    group: str = Field(description="Группа, 4 знака: 24.10")


class Ved(Base):
    fts_flag: bool
    country: Optional[str] = None


class ChannelWeight(Base):
    name: Channel
    weight: float = Field(ge=0, le=1)
    source: ChannelSource
    evidence: str = Field(description="Чем подтверждён вес, для блока «Почему вам»")


class CounterpartyShare(Base):
    """Агрегат по сектору контрагентов. Без ID контрагентов и сумм (S-04)."""
    side: Literal["buyer", "supplier"]
    sector: str
    share: float = Field(ge=0, le=1, description="Доля оборота")


class Profile(Base):
    client_id: str
    okved: Okved
    sector: str
    revenue_bln_rub: Optional[float] = Field(default=None, description="Пусто, если нет данных")
    region: Optional[str] = None
    products: dict[str, bool] = Field(description="Флаги CL_PRODUCT_*: loan, leasing, deposit...")
    ved: Ved
    counterparties: list[CounterpartyShare] = Field(default_factory=list)
    top_counterparty_share: Optional[float] = Field(
        default=None, ge=0, le=1, description="Концентрация: доля крупнейшего контрагента")
    channels: list[ChannelWeight] = Field(min_length=1)
    factor_exposure: dict[str, Literal["low", "medium", "high"]] = Field(default_factory=dict)
    built_at: datetime


# ---------- Match: скор события для клиента (уровень II) ----------

class ChannelContribution(Base):
    channel: Channel
    weight: float = Field(ge=0, le=1, description="Вес канала в профиле")
    match: float = Field(ge=0, le=1, description="Насколько событие попадает в канал")
    contribution: float = Field(ge=0, description="Вклад в итоговый скор")


class Match(Base):
    client_id: str
    event_id: str
    score: float = Field(ge=0)
    breakdown: list[ChannelContribution] = Field(min_length=1, description="F2-02")
    passed_level: int = Field(ge=0, le=3, description="До какого уровня воронки дошло")


# ---------- Insight: карточка инсайта (раздел 7) ----------

class Statement(Base):
    text: str
    layer: Layer
    sources: list[SourceRef] = Field(default_factory=list)
    profile_features: list[str] = Field(
        default_factory=list, description="Признаки компании: «34% выручки от стройки»")

    @model_validator(mode="after")
    def check_basis(self) -> "Statement":
        # I-01: у факта и позиции банка обязательна ссылка,
        # вывод ИИ опирается хотя бы на источник или признак компании.
        if self.layer in (Layer.fact, Layer.bank) and not self.sources:
            raise ValueError(f"Утверждение слоя «{self.layer.value}» без источника: {self.text[:60]}")
        if self.layer == Layer.bank and not any(s.kind == SourceKind.bank_analytics for s in self.sources):
            raise ValueError("Позиция банка должна ссылаться на материал банка")
        if self.layer == Layer.ai and not (self.sources or self.profile_features):
            raise ValueError(f"Вывод ИИ ни на что не опирается: {self.text[:60]}")
        return self


class InsightBlocks(Base):
    """6 вопросов из кейса (слайд 14). Пустой блок = явно «нет данных»."""
    what: str
    why: str
    consequences: str
    actions: str
    bank_view: str = Field(description="Если аналитики нет, так и пишем: «нет данных банка»")
    basis: str


class Insight(Base):
    schema_version: str = SCHEMA_VERSION
    insight_id: str
    client_id: str
    event_id: str
    role: Role
    channel_label: str = Field(description="Метка в ленте: «через покупателей»")
    summary: str = Field(description="Свёрнутая карточка, до 120 слов (I-07)")
    blocks: InsightBlocks
    statements: list[Statement] = Field(min_length=1)
    risks: list[str] = Field(default_factory=list)
    opportunities: list[str] = Field(default_factory=list)
    next_action: Optional[str] = None
    confidence: float = Field(ge=0, le=1)
    bank_conflict: bool = Field(
        default=False, description="A-03: вывод расходится с позицией банка, уверенность понижена")
    run_id: str = Field(description="Связь с трассой (T-02)")

    @model_validator(mode="after")
    def check_quality(self) -> "Insight":
        if len(self.summary.split()) > 120:
            raise ValueError("Свёрнутая карточка длиннее 120 слов (I-07)")
        features = {f for s in self.statements for f in s.profile_features}
        if len(features) < 2:
            raise ValueError("В выводе меньше 2 признаков компании (I-02)")
        return self


# ---------- Trace: журнал агента (раздел 10.1) ----------

class TraceStep(Base):
    step: Literal["plan", "context", "search", "analysis", "generation", "check", "adapt"]
    tool: Optional[str] = None
    input_ref: Optional[str] = None
    output_ref: Optional[str] = None
    model: Optional[str] = None
    tokens_in: int = 0
    tokens_out: int = 0
    cost_rub: float = 0
    duration_ms: int = 0
    status: Literal["ok", "failed", "retried"] = "ok"


class CheckResult(Base):
    name: str
    passed: bool
    action: Optional[str] = Field(default=None, description="Что сделал агент, если не прошло")


class Trace(Base):
    run_id: str
    client_id: str
    event_id: str
    plan: list[str] = Field(min_length=1, description="A-01")
    steps: list[TraceStep] = Field(min_length=1)
    checks: list[CheckResult] = Field(default_factory=list, description="A-03")
    prompt_version: Optional[str] = Field(default=None, description="T-03")
    started_at: datetime
    finished_at: datetime


# ---------- Пример: металлург и сокращение льготной ипотеки ----------

NEWS = SourceRef(url="https://example.com/news/ipoteka", name="Деловое издание (пример)",
                 published_at="2026-10-20T09:00:00+03:00")
BANK = SourceRef(url="https://example.com/alfa/tactical-q3", name="Тактический взгляд на 3-й квартал 2026",
                 published_at="2026-07-01T10:00:00+03:00", kind="bank_analytics", fragment_id="tq3-07")

EXAMPLES = {
    "event": Event(
        event_id="E-0001", title="Правительство сокращает программу льготной ипотеки",
        summary="Лимиты льготной ипотеки снижаются со следующего квартала. Застройщики ждут падения продаж.",
        sources=[NEWS], type="regulation", sectors=["Строительство"], okved=["41", "43"],
        regions=["РФ"], factors=["key_rate"], direction="negative", horizon="medium",
        importance=4, collected_at="2026-10-20T10:00:00+03:00"),
    "profile": Profile(
        client_id="U_000123", okved={"section": "C", "class": "24", "group": "24.10"},
        sector="Металлургия", revenue_bln_rub=180, region="Липецкая обл.",
        products={"loan": True, "leasing": True, "factoring": False, "deposit": True},
        ved={"fts_flag": True, "country": "Турция"},
        counterparties=[{"side": "buyer", "sector": "Строительство", "share": 0.34},
                        {"side": "supplier", "sector": "Горнодобыча", "share": 0.41}],
        top_counterparty_share=0.22,
        channels=[{"name": "demand", "weight": 0.78, "source": "data",
                   "evidence": "34% входящего оборота от компаний с ОКВЭД 41-43"},
                  {"name": "financial", "weight": 0.6, "source": "data",
                   "evidence": "Есть кредит и лизинг в банке"}],
        factor_exposure={"key_rate": "high", "coal": "high", "usd_try": "medium"},
        built_at="2026-10-23T12:00:00+03:00"),
    "match": Match(
        client_id="U_000123", event_id="E-0001", score=0.78, passed_level=3,
        breakdown=[{"channel": "demand", "weight": 0.78, "match": 0.9, "contribution": 0.70},
                   {"channel": "financial", "weight": 0.6, "match": 0.13, "contribution": 0.08}]),
    "insight": Insight(
        insight_id="I-0001", client_id="U_000123", event_id="E-0001", role="cfo",
        channel_label="через покупателей",
        summary="Сокращение льготной ипотеки ударит по застройщикам, а они дают треть вашей выручки. "
                "Вероятно снижение спроса на арматуру и рост дебиторки в ближайшие 1-6 месяцев.",
        blocks={"what": "Лимиты льготной ипотеки снижаются со следующего квартала.",
                "why": "34% входящего оборота приходится на застройщиков.",
                "consequences": "Риск: падение заказов и задержки оплат. Возможность: переток спроса в инфраструктуру.",
                "actions": "Проверить концентрацию на крупнейшем покупателе, обсудить факторинг.",
                "bank_view": "Банк ждёт охлаждения потребительского кредитования.",
                "basis": "1 новость, 1 материал банка, 2 признака профиля."},
        statements=[
            {"text": "Лимиты льготной ипотеки снижаются со следующего квартала.", "layer": "fact",
             "sources": [NEWS]},
            {"text": "Банк ожидает охлаждения кредитования населения.", "layer": "bank", "sources": [BANK]},
            {"text": "Спрос на продукцию компании может снизиться через застройщиков.", "layer": "ai",
             "sources": [NEWS], "profile_features": ["34% выручки от стройки", "один покупатель даёт 22%"]}],
        risks=["Снижение заказов от застройщиков", "Рост дебиторской задолженности"],
        opportunities=["Переориентация на инфраструктурные проекты"],
        next_action="Обсудить с менеджером банка факторинг", confidence=0.72, run_id="R-0001"),
    "trace": Trace(
        run_id="R-0001", client_id="U_000123", event_id="E-0001",
        plan=["Взять профиль", "Взять событие и скор", "Найти аналитику банка", "Построить карточку", "Проверить"],
        steps=[{"step": "context", "tool": "get_profile", "duration_ms": 40},
               {"step": "search", "tool": "search_bank_analytics", "duration_ms": 300},
               {"step": "generation", "model": "GLM-5.3-Flash", "tokens_in": 3200, "tokens_out": 600,
                "cost_rub": 0.4, "duration_ms": 9000}],
        checks=[{"name": "Все утверждения с источником", "passed": True},
                {"name": "Не меньше 2 признаков компании", "passed": True}],
        prompt_version="interp-v1",
        started_at="2026-10-23T12:05:00+03:00", finished_at="2026-10-23T12:05:10+03:00"),
}


if __name__ == "__main__":
    print(f"Контракт v{SCHEMA_VERSION}: все примеры прошли проверку")

    schema = {name: model.model_json_schema(by_alias=True)
              for name, model in [("Event", Event), ("Profile", Profile), ("Match", Match),
                                  ("Insight", Insight), ("Trace", Trace)]}
    with open("contract.schema.json", "w", encoding="utf-8") as f:
        json.dump(schema, f, ensure_ascii=False, indent=2)

    mocks = {k: v.model_dump(mode="json", by_alias=True) for k, v in EXAMPLES.items()}
    with open("mock_examples.json", "w", encoding="utf-8") as f:
        json.dump(mocks, f, ensure_ascii=False, indent=2)
    print("Сохранено: contract.schema.json, mock_examples.json")
