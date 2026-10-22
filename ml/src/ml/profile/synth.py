"""Синтетический набор в формате реальных CSV (разделитель ';') — для тестов и локальной отладки без данных кейса."""
from __future__ import annotations

from pathlib import Path

import numpy as np
import pandas as pd

U_COLS = ["SYNTHETIC_U", "ORG_TYPE", "OKFS_NAME", "COMPANY_AGE_YEARS_GRADATION", "ECO_MARK_LABEL", "MAINOKVED_CODE",
          "MAINOKVED_NAME", "NEGATIVEMARK", "AMOUNT_WORKERS_FNS_GRADATION", "AMOUNT_WORKERS_SPARK_GRADATION",
          "CL_TYPE", "CL_PRODUCT_RKO_MARK", "CL_PRODUCT_GAR_MARK", "CL_PRODUCT_LOA_MARK", "CL_PRODUCT_LEA_MARK",
          "CL_PRODUCT_DEP_MARK", "CL_PRODUCT_ZPP_MARK", "FTS_FLAG", "FTS_COUNTRY_NAME", "FACTORING_MARK",
          "REVENUE_COMPANY", "GROUP_ID", "CITY", "REGION"]


def _row(uid, okved, rev, region="Москва", group=None, **flags):
    r = {c: "" for c in U_COLS}
    r.update({"SYNTHETIC_U": uid, "MAINOKVED_CODE": okved, "REVENUE_COMPANY": str(rev), "GROUP_ID": group or f"G_{uid}",
              "REGION": region, "CITY": region, "NEGATIVEMARK": "0", "FTS_FLAG": "0", "CL_PRODUCT_RKO_MARK": "1",
              "CL_PRODUCT_GAR_MARK": "0", "CL_PRODUCT_LOA_MARK": "0", "CL_PRODUCT_LEA_MARK": "0",
              "CL_PRODUCT_DEP_MARK": "0", "CL_PRODUCT_ZPP_MARK": "0", "FACTORING_MARK": "0"})
    r.update({k: str(v) for k, v in flags.items()})
    return r


def make_synthetic(out_dir: str | Path, seed: int = 0) -> dict:
    rng = np.random.default_rng(seed)
    out = Path(out_dir)
    out.mkdir(parents=True, exist_ok=True)
    rows = [
        _row("U_5658", "46.51", 20e9, "Москва", CL_PRODUCT_LOA_MARK=1, CL_PRODUCT_LEA_MARK=1),
        _row("U_4909", "24.10", 30e9, "Липецкая область", FTS_FLAG=1, CL_PRODUCT_GAR_MARK=1),
        _row("U_9748", "01.11", 18e9, "Краснодарский край", FTS_FLAG=1, FACTORING_MARK=1),
        _row("U_25835", "47.11", 16e9, "Санкт-Петербург", CL_PRODUCT_DEP_MARK=1),
        _row("U_5639", "41.2", 25e9, "Москва", CL_PRODUCT_LOA_MARK=1, CL_PRODUCT_GAR_MARK=1),
        _row("U_LOW1", "62.01", 1e8, "Москва"),                              # ниже порога
        _row("U_GRP1", "46.7", 17e9, "Москва", group="G_SAME"),
        _row("U_GRP2", "46.7", 2e9, "Москва", group="G_SAME"),               # внутригрупповой контрагент
        _row("U_428697", "01.11", 99e9),                                      # мусорный идентификатор
        _row("U_428697", "64.19", 99e9),
    ]
    sector_codes = {"construction": "41.20", "realty": "68.20", "metal": "24.10", "transport": "49.41",
                    "energy": "35.11", "retail": "47.11", "food": "10.11", "chem": "20.15", "machine": "28.99"}
    cps: dict[str, list[str]] = {}
    for key, code in sector_codes.items():
        cps[key] = []
        for i in range(8):
            uid = f"U_{key[:3].upper()}{i}"
            rows.append(_row(uid, code, 1e9, "Москва"))
            cps[key].append(uid)
    pays = []

    def pay(payer, benef, amt, cnt=10):
        pays.append({"BENEFICIARY": benef, "PAYER": payer, "PAY_AMT": f"{amt:.2f}".replace(".", ","), "PAY_CNT": cnt})

    # U_5658: покупатели из строительства и недвижимости, поставщики из металлургии и транспорта
    for u in cps["construction"][:5]:
        pay(u, "U_5658", rng.uniform(5e6, 9e6))
    for u in cps["realty"][:3]:
        pay(u, "U_5658", rng.uniform(3e6, 5e6))
    for u in cps["metal"][:4]:
        pay("U_5658", u, rng.uniform(2e6, 6e6))
    for u in cps["transport"][:2]:
        pay("U_5658", u, rng.uniform(1e6, 2e6))
    pay("U_UNKNOWN_1", "U_5658", 6e6)                                         # контрагент без записи в df_Uid
    pay("U_5658", "U_5658", 9e9)                                              # перевод самому себе — исключить
    # U_4909: покупатели из машиностроения (один крупный), поставщики энергетика/транспорт
    for i, u in enumerate(cps["machine"][:4]):
        pay(u, "U_4909", 4e7 if i == 0 else 4e6)
    for u in cps["energy"][:3]:
        pay("U_4909", u, 5e6)
    for u in cps["transport"][:3]:
        pay("U_4909", u, 3e6)
    pay("U_4909", "U_4909", 5e9)
    # U_9748: покупатели — ритейл, поставщики — химия
    for u in cps["retail"][:5]:
        pay(u, "U_9748", 7e6)
    for u in cps["chem"][:3]:
        pay("U_9748", u, 5e6)
    # U_25835: почти нет известных контрагентов (покрытие мало)
    pay("U_X1", "U_25835", 9e6)
    pay("U_X2", "U_25835", 9e6)
    pay(cps["food"][0], "U_25835", 1e6)
    pay("U_25835", "U_X3", 8e6)
    # U_5639: стройка покупает у металлургии и оптовиков
    for u in cps["realty"][:4]:
        pay(u, "U_5639", 6e6)
    for u in cps["metal"][:3]:
        pay("U_5639", u, 7e6)
    # внутригруппово и мусор
    pay("U_GRP2", "U_GRP1", 5e8)
    pay("U_GRP1", "U_GRP2", 1e8)
    pay(cps["retail"][0], "U_GRP1", 3e6)
    pay("U_428697", "U_5658", 1e9)
    pay("U_5658", "U_428697", 1e9)
    pd.DataFrame(rows, columns=U_COLS).to_csv(out / "df_Uid_data_hack.csv", sep=";", index=False)
    pd.DataFrame(pays).to_csv(out / "df_Uid_payment_hack.csv", sep=";", index=False)
    pd.DataFrame([{"SYNTHETIC_F": "F_1", "CITY_NAME": "Москва", "RC_NAME": "РЦ", "SYNTHETIC_U": "U_5658"}]).to_csv(
        out / "df_Fid_Uid_link_hack.csv", sep=";", index=False)
    return {"dir": str(out), "above_threshold": ["U_4909", "U_5639", "U_5658", "U_9748", "U_GRP1", "U_25835"]}
