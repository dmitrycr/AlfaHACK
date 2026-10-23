import json
import subprocess
import sys
from pathlib import Path

import pytest

from ml.company_client import CompanyClient
from ml.push import push_profiles

ROOT = Path(__file__).parents[1]


def doc(cid, weight=0.5, **extra):
    return {"company_id": cid, "version": 1, "ruleset_version": "ml-stage1-v1",
            "channels": [{"name": "industry", "weight": weight, "source": "data", "evidence": ["e"]}],
            "factor_exposure": {}, "coverage": None, "built_at": "2026-10-21T00:00:00Z", "synthetic": False, **extra}


def test_dry_run_writes_nothing(fake):
    svc, addr = fake
    res = push_profiles(CompanyClient(addr), [doc("A"), doc("B")])
    assert svc.replace_calls == 0 and svc.profiles == {}
    assert res.pushed == 2 and "был бы записан v1" in res.lines[0]


def test_apply_uses_last_plus_one(fake):
    svc, addr = fake
    svc.profiles["A"] = [{"company_id": "A", "version": 1, "channels": []}, {"company_id": "A", "version": 2, "channels": []}]
    res = push_profiles(CompanyClient(addr), [doc("A"), doc("B")], apply=True)
    assert [p["version"] for p in svc.profiles["A"]] == [1, 2, 3]
    assert [p["version"] for p in svc.profiles["B"]] == [1]
    assert res.pushed == 2 and res.failed == 0


def test_rerun_no_duplicates_and_change_makes_new_version(fake):
    svc, addr = fake
    c = CompanyClient(addr)
    push_profiles(c, [doc("A")], apply=True)
    again = push_profiles(c, [doc("A")], apply=True)
    assert again.unchanged == 1 and again.pushed == 0 and len(svc.profiles["A"]) == 1
    assert push_profiles(c, [doc("A", weight=0.9)], apply=True).pushed == 1
    assert [p["version"] for p in svc.profiles["A"]] == [1, 2]


def test_error_on_one_client_does_not_stop_others(fake):
    svc, addr = fake
    svc.fail_for.add("B")
    res = push_profiles(CompanyClient(addr), [doc("A"), doc("B"), doc("C")], apply=True)
    assert (res.pushed, res.failed) == (2, 1) and "ОШИБКА" in res.lines[1] and "UNAVAILABLE" in res.lines[1]
    assert "B" not in svc.profiles and set(svc.profiles) == {"A", "C"}


@pytest.mark.parametrize("flag", [False, True])
def test_features_respect_flag(fake, flag):
    svc, addr = fake
    f = {"buyers": None, "suppliers": None}
    push_profiles(CompanyClient(addr), [doc("A", features=f)], apply=True, include_features=flag)
    assert ("features" in svc.profiles["A"][0]) is flag
    # повторный запуск при том же флаге не плодит версии
    assert push_profiles(CompanyClient(addr), [doc("A", features=f)], apply=True, include_features=flag).unchanged == 1


def _run(tmp_path, addr, *flags):
    src = tmp_path / "profiles.json"
    src.write_text(json.dumps([doc("A", features={"buyers": None, "suppliers": None})]), encoding="utf-8")
    return subprocess.run([sys.executable, str(ROOT / "scripts" / "push_profiles.py"), "--src", str(src), "--address", addr, *flags],
                          capture_output=True, text=True, encoding="utf-8", cwd=ROOT)


def test_cli_default_is_dry_run_and_apply_writes(fake, tmp_path):
    svc, addr = fake
    r = _run(tmp_path, addr)
    assert r.returncode == 0 and "DRY-RUN" in r.stdout and svc.profiles == {}
    r = _run(tmp_path, addr, "--apply")
    assert r.returncode == 0 and "APPLY" in r.stdout and "features" not in svc.profiles["A"][0]   # флаг выключен в config.yaml
    r = _run(tmp_path, addr, "--apply", "--include-features")       # профиль на сервере уже есть без features: не дублируем
    assert "features: да" in r.stdout and r.returncode == 0 and len(svc.profiles["A"]) == 1
    svc.profiles.clear()
    assert _run(tmp_path, addr, "--apply", "--include-features").returncode == 0 and "features" in svc.profiles["A"][0]
    svc.profiles.clear()
    svc.fail_for.add("A")
    r = _run(tmp_path, addr, "--apply")
    assert r.returncode == 1 and "ОШИБКА" in r.stdout and svc.profiles == {}
