"""Компонент G: построение профиля клиента по данным Альфа-Бизнеса (раздел 13). Без LLM на обычном запросе."""
from __future__ import annotations

import hashlib
import json
import statistics
from dataclasses import dataclass
from pathlib import Path

import pandas as pd

from ..refdata import RefData, load_refdata, normalize_okved
from ..schemas import Channel, ProfileIn

GARBAGE_IDS = {"U_428697"}
PRODUCT_COLS = {"loan": "CL_PRODUCT_LOA_MARK", "leasing": "CL_PRODUCT_LEA_MARK", "guarantee": "CL_PRODUCT_GAR_MARK",
                "deposit": "CL_PRODUCT_DEP_MARK", "factoring": "FACTORING_MARK", "rko": "CL_PRODUCT_RKO_MARK",
                "zpp": "CL_PRODUCT_ZPP_MARK"}
PRODUCT_EVIDENCE = {"loan": "CL_PRODUCT_LOA_MARK = 1", "leasing": "CL_PRODUCT_LEA_MARK = 1",
                    "guarantee": "CL_PRODUCT_GAR_MARK = 1", "deposit": "CL_PRODUCT_DEP_MARK = 1",
                    "factoring": "FACTORING_MARK = 1"}
PRODUCT_RU = {"loan": "есть кредит", "leasing": "есть лизинг", "guarantee": "есть гарантии",
              "deposit": "есть депозит", "factoring": "есть факторинг"}


def _truthy(v) -> bool:
    return str(v).strip().lower() in ("1", "1.0", "true", "да", "y")


def _num(series: pd.Series) -> pd.Series:
    return pd.to_numeric(series.astype(str).str.replace(",", ".").str.replace(r"\s", "", regex=True), errors="coerce")


# ---------------- каналы по правилам (13.3) ----------------
def make_channels(*, sector: str, okved: dict, region: str, products: dict[str, bool], fts_flag: bool,
                  commodities: list[str], buyers: dict[str, float], suppliers: dict[str, float],
                  coverage: dict, ref: RefData, cfg: dict) -> list[Channel]:
    w = cfg["profile"]["channel_weights"]
    ch: list[Channel] = [
        Channel(name="отраслевой", weight=w["отраслевой"], source="data", evidence=f"ОКВЭД {okved.get('group')} -> {sector}"),
        Channel(name="конкурент", weight=w["конкурент"], source="data", evidence=f"сектор {sector}"),
    ]
    fw = w["финансовый"]
    if products.get("loan") or products.get("leasing"):
        key = "loan" if products.get("loan") else "leasing"
        ch.append(Channel(name="финансовый", weight=fw["loan_or_leasing"], source="data", evidence=PRODUCT_RU[key]
                          + (" и лизинг" if products.get("loan") and products.get("leasing") else "")))
    elif products.get("guarantee") or products.get("factoring"):
        key = "guarantee" if products.get("guarantee") else "factoring"
        ch.append(Channel(name="финансовый", weight=fw["guarantee_or_factoring"], source="data", evidence=PRODUCT_RU[key]))
    elif products.get("deposit"):
        ch.append(Channel(name="финансовый", weight=fw["deposit_only"], source="data", evidence="только депозит"))
    else:
        ch.append(Channel(name="финансовый", weight=fw["none"], source="data", evidence="нет кредитных продуктов"))
    ch.append(Channel(name="валютный", weight=w["валютный"]["fts" if fts_flag else "none"], source="data",
                      evidence=f"FTS_FLAG = {int(fts_flag)}"))
    if commodities:
        ch.append(Channel(name="сырьевой", weight=w["сырьевой"], source="data",
                          evidence="сырьё сектора: " + ", ".join(commodities)))
    ch.append(Channel(name="региональный", weight=w["региональный"], source="data", evidence=f"регион {region}"))
    ch.append(Channel(name="регуляторный", weight=w["регуляторный"], source="data", evidence=f"отрасль {sector}"))
    cap = cfg["ranking"]["payments_F_cap"]
    for role, name, shares, cov_key, word in (("demand", "спрос", buyers, "buyers", "покупателей"),
                                              ("supply", "предложение", suppliers, "suppliers", "поставщиков")):
        cov = float(coverage.get(cov_key) or 0.0)
        data = {s: min(1.0, v / cap) for s, v in shares.items() if v > 0}
        mat = dict(ref.demand.get(sector, {})) if role == "demand" else dict(ref.supply.get(sector, {}))
        top_mat = dict(sorted(mat.items(), key=lambda kv: -kv[1])[:3])
        top_data = dict(sorted(data.items(), key=lambda kv: -kv[1])[:3])
        chosen: dict[str, tuple[float, str]] = {}
        if cov >= cfg["ranking"]["coverage_payments_only"]:
            chosen = {s: (v, "data") for s, v in top_data.items()}
        elif cov >= cfg["ranking"]["coverage_matrix_only"]:
            for s in set(top_data) | set(top_mat):
                d, m = top_data.get(s, 0.0), top_mat.get(s, 0.0)
                chosen[s] = (d, "data") if d >= m else (m, "matrix")
        else:
            chosen = {s: (v, "matrix") for s, v in top_mat.items()}
        for s, (v, src) in sorted(chosen.items(), key=lambda kv: -kv[1][0]):
            ev = (f"{round(shares.get(s, 0) * 100)}% оборота — контрагенты сектора {s}" if src == "data"
                  else f"матрица связей: {sector} -> {s} ({word})")
            ch.append(Channel(name=name, weight=round(min(1.0, v), 4), source=src, evidence=ev, sector=s))
    return ch


# ---------------- загрузка и очистка (13.1) ----------------
@dataclass
class Data:
    u: pd.DataFrame
    pay: pd.DataFrame
    group_size: pd.Series


def load_data(data_dir: str | Path) -> Data:
    d = Path(data_dir)
    u = pd.read_csv(d / "df_Uid_data_hack.csv", sep=";", dtype=str, encoding="utf-8-sig")
    pay = pd.read_csv(d / "df_Uid_payment_hack.csv", sep=";", dtype={"PAYER": str, "BENEFICIARY": str}, encoding="utf-8-sig")
    u = u[~u["SYNTHETIC_U"].isin(GARBAGE_IDS)].drop_duplicates("SYNTHETIC_U", keep="first").set_index("SYNTHETIC_U", drop=False)
    pay["PAY_AMT"] = _num(pay["PAY_AMT"])
    pay = pay.dropna(subset=["PAY_AMT"])
    pay = pay[~pay["PAYER"].isin(GARBAGE_IDS) & ~pay["BENEFICIARY"].isin(GARBAGE_IDS)]
    pay = pay[pay["PAYER"] != pay["BENEFICIARY"]]                  # 82% суммы — переводы самому себе
    gs = u.groupby("GROUP_ID")["SYNTHETIC_U"].transform("size")
    gsize = pd.Series(gs.values, index=u.index)
    return Data(u=u, pay=pay.reset_index(drop=True), group_size=gsize)


class ProfileBuilder:
    def __init__(self, data_dir: str | Path, cfg: dict, ref: RefData | None = None):
        self.cfg, self.ref = cfg, ref or load_refdata()
        self.data_dir = Path(data_dir)
        self.d = load_data(self.data_dir)
        u = self.d.u
        self.okved = {cid: normalize_okved(code) for cid, code in u["MAINOKVED_CODE"].items()}
        self.sector = {cid: self.ref.sector_of_class(o["class"]) for cid, o in self.okved.items()}
        self.group = u["GROUP_ID"].to_dict()
        self.gsize = self.d.group_size.to_dict()
        self.region = u["REGION"].fillna("").to_dict()
        self.revenue = _num(u["REVENUE_COMPANY"]).to_dict()

    # ---- платежи ----
    def _side(self, client: str, rows: pd.DataFrame, cp_col: str) -> dict:
        """rows: платежи по одной стороне клиента. cp_col: колонка контрагента."""
        g = self.group.get(client)
        intra_mask = rows[cp_col].map(lambda x: (self.group.get(x) == g and self.gsize.get(x, 1) > 1)
                                      if x in self.group else False).astype(bool)  # пустой rows -> не object-маска
        total_all = float(rows["PAY_AMT"].sum())
        intragroup = float(rows.loc[intra_mask, "PAY_AMT"].sum())
        rows = rows[~intra_mask]
        total = float(rows["PAY_AMT"].sum())
        res = {"shares": {}, "coverage": 0.0, "unknown_share": 0.0, "top": None,
               "intragroup_share": (intragroup / total_all) if total_all > 0 else 0.0}
        if total <= 0:
            return res
        by_cp = rows.groupby(cp_col)["PAY_AMT"].sum()
        sec_amt: dict[str, float] = {}
        unknown = 0.0
        for cp, amt in by_cp.items():
            if cp in self.sector:
                sec_amt[self.sector[cp]] = sec_amt.get(self.sector[cp], 0.0) + float(amt)
            else:
                unknown += float(amt)
        res["shares"] = {s: a / total for s, a in sorted(sec_amt.items(), key=lambda kv: -kv[1])}
        res["unknown_share"] = unknown / total
        res["coverage"] = 1.0 - unknown / total
        pc = self.cfg["profile"]
        if len(rows) >= int(pc["concentration_min_payments"]) and res["coverage"] >= float(pc["concentration_min_coverage"]):
            top_cp = by_cp.idxmax()
            res["top"] = {"share": float(by_cp.max()) / total,
                          "sector": self.sector.get(top_cp), "region": self.region.get(top_cp) or None}
        return res

    def build_profile(self, client_id: str, pay_index: dict | None = None) -> ProfileIn:
        u = self.d.u.loc[client_id]
        okv = self.okved[client_id]
        sector = self.sector[client_id]
        pay = self.d.pay
        if pay_index is not None:
            b_rows = pay_index["benef"].get(client_id, pay.iloc[0:0])
            s_rows = pay_index["payer"].get(client_id, pay.iloc[0:0])
        else:
            b_rows, s_rows = pay[pay["BENEFICIARY"] == client_id], pay[pay["PAYER"] == client_id]
        buy = self._side(client_id, b_rows, "PAYER")           # покупатели платят клиенту
        sup = self._side(client_id, s_rows, "BENEFICIARY")     # клиент платит поставщикам
        products = {k: _truthy(u.get(col)) for k, col in PRODUCT_COLS.items()}
        fts = _truthy(u.get("FTS_FLAG"))
        commodities = self.ref.commodities_of(sector)
        coverage = {"buyers": round(buy["coverage"], 4), "suppliers": round(sup["coverage"], 4),
                    "intragroup_buyers": round(buy["intragroup_share"], 4),
                    "intragroup_suppliers": round(sup["intragroup_share"], 4)}
        tops = [t for t in (buy["top"], sup["top"]) if t]
        conc = max(tops, key=lambda t: t["share"]) if tops else None
        buyers = {s: round(v, 4) for s, v in buy["shares"].items()}
        suppliers = {s: round(v, 4) for s, v in sup["shares"].items()}
        rev = self.revenue.get(client_id)
        return ProfileIn(
            client_id=client_id, sector=sector, okved=okv, region=str(self.region.get(client_id) or ""),
            revenue=None if rev is None or pd.isna(rev) else float(rev), products=products,
            ved={"fts_flag": fts, "country": None},               # FTS_COUNTRY_NAME пуст на 90%: не используем
            channels=make_channels(sector=sector, okved=okv, region=str(self.region.get(client_id) or ""),
                                   products=products, fts_flag=fts, commodities=commodities, buyers=buyers,
                                   suppliers=suppliers, coverage=coverage, ref=self.ref, cfg=self.cfg),
            buyers=buyers, suppliers=suppliers,
            concentration=({"share": round(conc["share"], 4), "sector": conc["sector"], "region": conc["region"]}
                           if conc else None),
            commodities=commodities, coverage=coverage)

    def clients_above_threshold(self) -> tuple[list[str], list[str]]:
        thr = float(self.cfg["profile"]["revenue_threshold"])
        above = [c for c, r in self.revenue.items() if r is not None and not pd.isna(r) and r >= thr]
        below = [c for c in self.revenue if c not in set(above)]
        return sorted(above), sorted(below)

    def build_all(self) -> dict:
        above, below = self.clients_above_threshold()
        aset = set(above)
        sub = self.d.pay[self.d.pay["BENEFICIARY"].isin(aset) | self.d.pay["PAYER"].isin(aset)]
        idx = {"benef": {k: v for k, v in sub[sub["BENEFICIARY"].isin(aset)].groupby("BENEFICIARY")},
               "payer": {k: v for k, v in sub[sub["PAYER"].isin(aset)].groupby("PAYER")}}
        profiles = {c: self.build_profile(c, idx).model_dump() for c in above}
        return {"profiles": profiles, "below_threshold": below, "report": coverage_report(profiles)}


def coverage_report(profiles: dict[str, dict]) -> dict:
    if not profiles:
        return {"clients": 0}
    cb = [p["coverage"].get("buyers", 0.0) for p in profiles.values()]
    cs = [p["coverage"].get("suppliers", 0.0) for p in profiles.values()]
    both = [min(a, b) for a, b in zip(cb, cs)]
    conc = sorted(((p["concentration"]["share"], cid) for cid, p in profiles.items() if p.get("concentration")),
                  reverse=True)[:5]
    return {"clients": len(profiles),
            "share_buyers_cov_ge_0.5": round(sum(c >= 0.5 for c in cb) / len(cb), 4),
            "share_suppliers_cov_ge_0.5": round(sum(c >= 0.5 for c in cs) / len(cs), 4),
            "share_both_cov_ge_0.5": round(sum(c >= 0.5 for c in both) / len(both), 4),
            "median_cov_buyers": round(statistics.median(cb), 4),
            "median_cov_suppliers": round(statistics.median(cs), 4),
            "top5_concentration": [{"client_id": cid, "share": s} for s, cid in conc]}


def input_hash(data_dir: str | Path, cfg: dict) -> str:
    h = hashlib.sha256()
    for name in ("df_Uid_data_hack.csv", "df_Uid_payment_hack.csv", "df_Fid_Uid_link_hack.csv"):
        p = Path(data_dir) / name
        if p.exists():
            with open(p, "rb") as f:
                for chunk in iter(lambda: f.read(1 << 20), b""):
                    h.update(chunk)
    h.update(json.dumps(cfg["profile"], sort_keys=True).encode())
    h.update(json.dumps(cfg["ranking"], sort_keys=True).encode())
    return h.hexdigest()


def build_and_cache(data_dir: str | Path, out_path: str | Path, cfg: dict, force: bool = False) -> dict:
    """Пересчёт только при изменении входных файлов (PR-09)."""
    out = Path(out_path)
    h = input_hash(data_dir, cfg)
    if out.exists() and not force:
        cached = json.loads(out.read_text(encoding="utf-8"))
        if cached.get("input_hash") == h:
            cached["from_cache"] = True
            return cached
    res = ProfileBuilder(data_dir, cfg).build_all()
    res["input_hash"] = h
    out.parent.mkdir(parents=True, exist_ok=True)
    out.write_text(json.dumps(res, ensure_ascii=False, indent=1), encoding="utf-8")
    res["from_cache"] = False
    return res
