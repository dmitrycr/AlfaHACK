import pandas as pd
import pytest

from ml.profile.features import side_features

SECT = {"A": "Строительство", "B": "Строительство", "C": "Торговля", "D": "Торговля", "E": "Торговля", "F": "Металлургия"}
KW = dict(min_coverage=0.3, min_counterparties=5)


def _s(d):
    return pd.Series(d, dtype=float)


def test_hhi_top5_three_counterparties():
    f = side_features(_s({"A": 50, "B": 30, "C": 20}), SECT, min_coverage=0.3, min_counterparties=3)
    assert f["concentration"] == {"top1_share": 0.5, "top5_share": 1.0, "hhi": 0.38}
    assert f["sector_shares"] == [{"sector": "Строительство", "share": 0.8}, {"sector": "Торговля", "share": 0.2}]


def test_top5_six_counterparties():
    f = side_features(_s({"A": 40, "B": 20, "C": 15, "D": 10, "E": 10, "F": 5}), SECT, **KW)
    c = f["concentration"]
    assert c["top1_share"] == 0.4 and c["top5_share"] == 0.95
    assert c["hhi"] == pytest.approx(0.16 + 0.04 + 0.0225 + 0.01 + 0.01 + 0.0025, abs=1e-4)


def test_concentration_null_below_min_counterparties_but_shares_kept():
    f = side_features(_s({"A": 50, "B": 30, "C": 20}), SECT, **KW)
    assert f["concentration"] is None and len(f["sector_shares"]) == 2


def test_no_payments_is_null():
    assert side_features(_s({}), SECT, **KW) is None
    assert side_features(_s({"A": 0.0}), SECT, **KW) is None


def test_low_coverage_is_null():
    # известных контрагентов 20% суммы
    assert side_features(_s({"A": 20, "X1": 50, "X2": 30}), SECT, **KW) is None


def test_unknown_excluded_from_sector_shares_but_in_concentration():
    f = side_features(_s({"A": 40, "C": 40, "X1": 10, "X2": 5, "X3": 5}), SECT, **KW)
    assert f["sector_shares"] == [{"sector": "Строительство", "share": 0.5}, {"sector": "Торговля", "share": 0.5}]
    assert f["concentration"]["top1_share"] == 0.4        # от всей суммы стороны, включая неизвестных


def test_limit_and_rounding_and_order():
    sect = {f"c{i}": f"S{i:02d}" for i in range(12)}
    f = side_features(_s({f"c{i}": 12 - i for i in range(12)}), sect, **KW)
    sh = f["sector_shares"]
    assert len(sh) == 10 and [x["share"] for x in sh] == sorted((x["share"] for x in sh), reverse=True)
    assert sh[0] == {"sector": "S00", "share": round(12 / 78, 3)}
