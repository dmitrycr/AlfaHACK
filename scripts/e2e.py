#!/usr/bin/env python3
"""Exercise the actual gateway and five services. News providers remain fixtures."""
import http.cookiejar
import json
import os
from pathlib import Path
import subprocess
import time
import urllib.error
import urllib.request
import uuid

ROOT = Path(__file__).resolve().parents[1]

def validate_response(method,path,status,payload):
    if os.environ.get("E2E_VALIDATE_CONTRACTS")!="true" or payload is None:
        return
    from jsonschema import Draft202012Validator, FormatChecker
    route=path.split("?",1)[0]
    name="Error" if status>=400 else None
    if status<400:
        if route in {"/api/v1/auth/login","/api/v1/auth/me"}:name="AuthResponse"
        elif route=="/api/v1/companies/current":name="Company"
        elif route=="/api/v1/companies/current/profile":name="Profile"
        elif route.startswith("/api/v1/news/submissions"):name="Submission"
        elif route=="/api/v1/news":name="ArticlePage"
        elif route.startswith("/api/v1/news/"):name="Article"
        elif route=="/api/v1/events":name="EventPage"
        elif route.startswith("/api/v1/events/"):name="Event"
        elif route=="/api/v1/analytics/summary":name="AnalyticsSummary"
    if name:
        api=json.loads((ROOT/"contracts/openapi/backend.yaml").read_text())
        schema={"$ref":"#/components/schemas/"+name,"components":api["components"]}
        Draft202012Validator(schema,format_checker=FormatChecker()).validate(payload)


class Client:
    def __init__(self, base):
        self.base = base.rstrip("/")
        self.opener = urllib.request.build_opener(
            urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar())
        )
        self.csrf = ""

    def request(self, method, path, body=None, headers=None, expected=200):
        data = None if body is None else json.dumps(body).encode()
        request_headers = {"Content-Type": "application/json"}
        if self.csrf:
            request_headers["X-CSRF-Token"] = self.csrf
        request_headers.update(headers or {})
        request = urllib.request.Request(
            self.base + path, data=data, method=method, headers=request_headers
        )
        try:
            response = self.opener.open(request, timeout=10)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            payload = response.read()
            assert response.status == expected, (method, path, response.status, payload[:300])
            value=json.loads(payload) if payload else None
            validate_response(method,path,response.status,value)
            return value

    def login(self, email, password):
        result = self.request("POST", "/api/v1/auth/login", {"email": email, "password": password})
        self.csrf = result["csrf_token"]
        assert len(self.csrf) == 64
        return result


def eventually(getter, predicate, description, timeout=40):
    deadline = time.monotonic() + timeout
    last = None
    while time.monotonic() < deadline:
        try:
            last = getter()
            if predicate(last):
                return last
        except (urllib.error.URLError, AssertionError, TimeoutError) as error:
            last = str(error)
        time.sleep(0.2)
    raise AssertionError(("timeout", description, last))


def run(base, password, collect):
    metal, build = Client(base), Client(base)
    assert metal.request("GET","/openapi.json")["openapi"]=="3.1.0"
    metal.request("GET", "/api/v1/companies/current", expected=401)
    metal.login("metal@example.test", password)
    build.login("build@example.test", password)
    assert metal.request("GET", "/api/v1/companies/current")["id"] == "company-metal"
    assert build.request("GET", "/api/v1/companies/current")["id"] == "company-build"
    metal.request("GET", "/api/v1/companies/current?company_id=company-build", expected=403)
    profile = metal.request("GET", "/api/v1/companies/current/profile")
    assert profile["synthetic"] and profile["coverage"] is None
    collect(False)

    def public_news():
        return [item for item in metal.request("GET", "/api/v1/news?limit=100")["items"] if item["scope"] == "public"]

    articles = eventually(public_news, lambda values: len(values) == 7, "seven public fixture publications")
    assert all(article["published_at"] and article["url"] for article in articles)
    assert len({article["duplicate_group"] for article in articles}) == 6
    public_events = eventually(
        lambda: [item for item in metal.request("GET", "/api/v1/events?limit=100")["items"] if item["scope"] == "public"],
        lambda values: len(values) == 6,
        "six candidate events",
    )
    assert all(item["status"] == "awaiting_tagging" for item in public_events)
    before = {item["id"]: item["revision"] for item in articles}
    collect(False)
    assert {item["id"]: item["revision"] for item in public_news()} == before

    collect(True)
    updated = eventually(public_news, lambda values: any(item["source_id"] == "rss-full" and item["revision"] > before[item["id"]] for item in values), "updated article revision")
    changed = [item for item in updated if item["revision"] != before[item["id"]]]
    assert len(changed) == 1 and "16%" in metal.request("GET", "/api/v1/news/" + changed[0]["id"])["text"]
    eventually(
        lambda: metal.request("GET", "/api/v1/events?limit=100")["items"],
        lambda values: any(any(ref["article_id"] == changed[0]["id"] and ref["revision"] == changed[0]["revision"] for ref in item["articles"]) for item in values),
        "updated event source revision",
    )

    key = uuid.uuid4().hex
    payload = {"title": "Ручная синтетическая новость", "text": "Частная тестовая загрузка компании. Дата публикации неизвестна, ссылка отсутствует."}
    submission = metal.request("POST", "/api/v1/news/submissions", payload, {"Idempotency-Key": key}, 202)
    duplicate = metal.request("POST", "/api/v1/news/submissions", payload, {"Idempotency-Key": key}, 202)
    assert duplicate["id"] == submission["id"]
    metal.request("POST", "/api/v1/news/submissions", dict(payload, text="Другой текст"), {"Idempotency-Key": key}, 409)
    job = eventually(lambda: metal.request("GET", "/api/v1/news/submissions/" + submission["id"]), lambda value: value["status"] == "stored", "manual submission")
    article = metal.request("GET", "/api/v1/news/" + job["article_id"])
    assert article["url"] == "" and article["published_at"] is None
    assert article["scope"] == "company:company-metal"
    build.request("GET", "/api/v1/news/submissions/" + job["id"], expected=404)
    build.request("GET", "/api/v1/news/" + article["id"], expected=404)
    private_event = eventually(lambda: metal.request("GET", "/api/v1/events?limit=100")["items"], lambda values: any(any(ref["article_id"] == article["id"] for ref in item["articles"]) for item in values), "private event")
    private_event = next(item for item in private_event if any(ref["article_id"] == article["id"] for ref in item["articles"]))
    build.request("GET", "/api/v1/events/" + private_event["id"], expected=404)
    summary = eventually(lambda: metal.request("GET", "/api/v1/analytics/summary"), lambda value: any(item["kind"] == "event.prepared" for item in value["counters"]), "analytics consumption")
    assert summary["source_mode"] == "fixture" and summary["llm_calls"] == 0
    metal.request("POST", "/api/v1/auth/logout", expected=204)
    metal.request("GET", "/api/v1/auth/me", expected=401)
    report = {"result": "passed", "public_articles": 7, "candidate_events": 6, "exact_duplicate_groups": 6, "manual_privacy": "passed", "updates_and_idempotency": "passed", "source_mode": "fixture"}
    print(json.dumps(report, ensure_ascii=False))
    return report


def compose_collect(updated):
    command = ["docker", "compose", "--env-file", ".env", "-f", "infrastructure/docker/compose.yaml", "exec", "-T"]
    if updated:
        command += ["-e", "SOURCE_CONFIG=/fixtures/sources-updated.json"]
    command += ["news-service", "/app", "collect"]
    subprocess.run(command, cwd=ROOT, check=True, timeout=60)


if __name__ == "__main__":
    run(os.environ.get("E2E_BASE_URL", "http://localhost:8080"), os.environ.get("DEMO_PASSWORD", "DemoOnly-2026!"), compose_collect)
