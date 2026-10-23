import json
from pathlib import Path

import pytest

from ml import contract
from ml.contract import Event, Insight, Match, Profile, Trace

EXAMPLES = json.loads((Path(__file__).parent / "fixtures" / "contract_examples.json").read_text(encoding="utf-8"))
CLASSES = {"event": Event, "profile": Profile, "match": Match, "insight": Insight, "trace": Trace}


def test_five_examples_present():
    assert set(EXAMPLES) == set(CLASSES)


@pytest.mark.parametrize("name", list(CLASSES))
def test_contract_example_valid(name):
    obj = CLASSES[name].model_validate(EXAMPLES[name])
    assert json.loads(obj.model_dump_json(by_alias=True))                     # сериализуется обратно


def test_contract_is_strict_and_versioned():
    assert contract.SCHEMA_VERSION == "0.1"
    with pytest.raises(Exception):                                            # extra="forbid"
        Event.model_validate({**EXAMPLES["event"], "noise": True})
    with pytest.raises(Exception):                                            # N-02: без источника события нет
        Event.model_validate({**EXAMPLES["event"], "sources": []})
