"""Признаки сторон (покупатели/поставщики) для поля features профиля Company Service.
Формат (от бэкенда): {"buyers": {"sector_shares": [{"sector","share"}], "concentration": {"top1_share","top5_share","hhi"}} | null,
"suppliers": ... | null}. Сырьё и coverage сюда не входят."""
from __future__ import annotations

from typing import Mapping

import pandas as pd

SECTOR_SHARES_LIMIT = 10
SHARE_DIGITS = 3
CONC_DIGITS = 4          # согласовано с бэкендом: top1_share/top5_share/hhi - 4 знака, sector_shares.share - 3


def side_features(by_cp: pd.Series, sector_of: Mapping[str, str], *, min_coverage: float, min_counterparties: int,
                  limit: int = SECTOR_SHARES_LIMIT) -> dict | None:
    """by_cp: сумма платежей по контрагенту (самопереводы и внутригрупповые уже исключены).
    None - платежей нет или покрытие справочником < min_coverage. concentration = None при контрагентах < min_counterparties."""
    total = float(by_cp.sum()) if len(by_cp) else 0.0
    if total <= 0:
        return None
    known = by_cp[by_cp.index.isin(list(sector_of))]
    known_total = float(known.sum())
    if known_total / total < min_coverage:
        return None
    by_sector = known.groupby(known.index.map(sector_of)).sum()          # нормировка на контрагентов из справочника
    shares = sorted(((s, round(float(v) / known_total, SHARE_DIGITS)) for s, v in by_sector.items()),
                    key=lambda kv: (-kv[1], kv[0]))
    shares = [kv for kv in shares if kv[1] > 0][:limit]               # сектора с долей 0.000 после округления не отдаём
    conc = None
    if len(by_cp) >= min_counterparties:
        cp = (by_cp / total).sort_values(ascending=False)                # доли от всех платежей стороны
        conc = {"top1_share": round(float(cp.iloc[0]), CONC_DIGITS), "top5_share": round(float(cp.iloc[:5].sum()), CONC_DIGITS),
                "hhi": round(float((cp ** 2).sum()), CONC_DIGITS)}
    return {"sector_shares": [{"sector": s, "share": v} for s, v in shares], "concentration": conc}
