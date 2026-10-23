#!/usr/bin/env python3
"""Validate checked-in contracts; requires jsonschema==4.25.1."""
import json
import subprocess
import sys
from pathlib import Path
from jsonschema import Draft202012Validator, FormatChecker

ROOT = Path(__file__).resolve().parents[1]
subprocess.run([sys.executable, str(ROOT / "tools/data/build_sector_rules.py"), "--check"], check=True)
schema = json.loads((ROOT / "contracts/events/backend.schema.json").read_text())
Draft202012Validator.check_schema(schema)
source_schema = json.loads((ROOT / "contracts/events/sources.schema.json").read_text())
Draft202012Validator.check_schema(source_schema)
for name in ("sources.json", "sources-updated.json"):
    config = json.loads((ROOT / "services/news-service/internal/source/fixtures" / name).read_text())
    Draft202012Validator(source_schema, format_checker=FormatChecker()).validate(config)
example = json.loads((ROOT / "docs/api/examples/profile.json").read_text())
profile_schema = dict(schema, **{"$ref": "#/$defs/Profile"})
Draft202012Validator(profile_schema, format_checker=FormatChecker()).validate(example)
features_example = json.loads((ROOT / "docs/api/examples/profile-features.json").read_text(encoding="utf-8"))
Draft202012Validator(profile_schema, format_checker=FormatChecker()).validate(features_example)
api = json.loads((ROOT / "contracts/openapi/backend.yaml").read_text())

scoring_schema = json.loads((ROOT / "contracts/events/scoring.schema.json").read_text(encoding="utf-8"))
Draft202012Validator.check_schema(scoring_schema)
for definition in schema["$defs"]:
    assert scoring_schema["$defs"][definition] == schema["$defs"][definition], "Shared scoring definitions drifted"
for name, definition in (("scoring-request.json", "RankingRequest"), ("scoring-result.json", "RankingResult")):
    example = json.loads((ROOT / "docs/api/examples" / name).read_text(encoding="utf-8"))
    Draft202012Validator(dict(scoring_schema, **{"$ref": "#/$defs/" + definition}), format_checker=FormatChecker()).validate(example)

def check_refs(node):
    if isinstance(node, dict):
        if "$ref" in node:
            target = api
            assert node["$ref"].startswith("#/"), "Only local references are allowed"
            for part in node["$ref"][2:].split("/"):
                target = target[part]
        for value in node.values():
            check_refs(value)
    elif isinstance(node, list):
        for value in node:
            check_refs(value)

check_refs(api)
api_profile = dict(api, **{"$ref": "#/components/schemas/Profile"})
Draft202012Validator(api_profile, format_checker=FormatChecker()).validate(features_example)
for name, component in api["components"]["schemas"].items():
    Draft202012Validator.check_schema(component)
registration = json.loads((ROOT / "docs/api/examples/registration.json").read_text())
Draft202012Validator(api["components"]["schemas"]["RegistrationInput"], format_checker=FormatChecker()).validate(registration)
assert api["paths"]["/auth/login"]["post"]["security"] == []
llm_schema = json.loads((ROOT / "contracts/events/llm.schema.json").read_text())
Draft202012Validator.check_schema(llm_schema)
examples = {"profile-input":"LLMProfileInput","profile-brief":"LLMProfileBrief","tag-input":"LLMTagInput", "tag-result":"LLMTagResult", "prepared-event":"LLMPrepared",
            "insight-context":"LLMInsightInput", "card":"LLMCard", "check-input":"LLMCheckInput",
            "check-result":"LLMCheckResult", "insight":"LLMInsight", "chat-input":"LLMChatInput",
            "chat-answer":"LLMAnswer", "match":"LLMMatch"}
for filename, name in examples.items():
    example = json.loads((ROOT / "docs/api/examples/llm" / (filename + ".json")).read_text())
    Draft202012Validator(dict(llm_schema, **{"$ref":"#/$defs/" + name}), format_checker=FormatChecker()).validate(example)
    Draft202012Validator({"$ref":"#/components/schemas/" + name, "components":api["components"]}, format_checker=FormatChecker()).validate(example)
print("Contracts validated: OpenAPI, backend/source schemas, registration/profile and 13 LLM examples")
