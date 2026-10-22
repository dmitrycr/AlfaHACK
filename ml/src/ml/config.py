"""Загрузка config.yaml и переменных окружения. Ключ API читается только из LLM_API_KEY и в конфиг не попадает."""
from __future__ import annotations

import copy
import os
import re
from pathlib import Path
from typing import Any

import yaml

ML_ROOT = Path(__file__).resolve().parents[2]
CONFIG_DIR = ML_ROOT / "config"

_ENV_RE = re.compile(r"\$\{([A-Z0-9_]+)\}")


def _expand(value: Any) -> Any:
    if isinstance(value, str):
        return _ENV_RE.sub(lambda m: os.environ.get(m.group(1), ""), value)
    if isinstance(value, dict):
        return {k: _expand(v) for k, v in value.items()}
    if isinstance(value, list):
        return [_expand(v) for v in value]
    return value


def _merge(base: dict, extra: dict) -> dict:
    for k, v in extra.items():
        if isinstance(v, dict) and isinstance(base.get(k), dict):
            _merge(base[k], v)
        else:
            base[k] = v
    return base


def load_config(path: str | Path | None = None, overrides: dict | None = None) -> dict:
    p = Path(path) if path else CONFIG_DIR / "config.yaml"
    with open(p, encoding="utf-8") as f:
        cfg = _expand(yaml.safe_load(f))
    if os.environ.get("LLM_MODE"):
        cfg["llm"]["mode"] = os.environ["LLM_MODE"].strip().lower()
    if os.environ.get("DATA_DIR"):
        cfg["paths"]["data_dir"] = os.environ["DATA_DIR"]
    if overrides:
        _merge(cfg, copy.deepcopy(overrides))
    return cfg


_CACHED: dict | None = None


def get_config() -> dict:
    global _CACHED
    if _CACHED is None:
        _CACHED = load_config()
    return _CACHED


def resolve_path(p: str | Path, base: Path | None = None) -> Path:
    path = Path(p)
    return path if path.is_absolute() else (base or ML_ROOT) / path
