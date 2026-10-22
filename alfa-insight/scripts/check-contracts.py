#!/usr/bin/env python3
"""Validate checked-in contracts; requires jsonschema==4.25.1."""
import json
from pathlib import Path
from jsonschema import Draft202012Validator, FormatChecker

ROOT = Path(__file__).resolve().parents[1]
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
api = json.loads((ROOT / "contracts/openapi/backend.yaml").read_text())

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
for name, component in api["components"]["schemas"].items():
    Draft202012Validator.check_schema(component)
assert api["paths"]["/auth/login"]["post"]["security"] == []
print("Contracts validated: OpenAPI references, JSON schemas, source configurations and profile example")
