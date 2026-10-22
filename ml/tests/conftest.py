import json
import os
from pathlib import Path

import pytest

os.environ["LLM_MODE"] = "mock"
os.environ.pop("LLM_API_KEY", None)
for var in ("PROFILE_SERVICE_URL", "EVENT_SERVICE_URL", "NEWS_SERVICE_URL", "KNOWLEDGE_SERVICE_URL", "ML_AS_OF"):
    os.environ.pop(var, None)

from ml.config import load_config  # noqa: E402
from ml.schemas import ProfileIn  # noqa: E402
try:  # модули других этапов (services, store) отсутствуют в ветке Этапа 1
    from ml.services.common import build_context  # noqa: E402
    from ml.store import Store  # noqa: E402
except ImportError:
    build_context = Store = None

FX = Path(__file__).parent / "fixtures"


@pytest.fixture
def cfg(tmp_path):
    return load_config(overrides={
        "llm": {"mode": "mock", "retry_pause_s": 0},
        "paths": {"bank_chunks": str(tmp_path / "chunks.jsonl"), "profiles": str(tmp_path / "no_profiles.json"),
                  "store": str(tmp_path / "store.sqlite")}})


@pytest.fixture
def store(tmp_path):
    return Store(tmp_path / "t.sqlite")


@pytest.fixture
def ctx(cfg, store):
    return build_context(cfg, store=store)


@pytest.fixture(scope="session")
def news30():
    return json.loads((FX / "news_30.json").read_text(encoding="utf-8"))


@pytest.fixture(scope="session")
def profiles():
    raw = json.loads((FX / "profiles_demo.json").read_text(encoding="utf-8"))["profiles"]
    return {k: ProfileIn.model_validate(v) for k, v in raw.items()}


@pytest.fixture
def tagged(ctx, news30):
    return [ctx.tagger.tag(n) for n in news30]
