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
        elif route=="/api/v1/auth/register":name="RegistrationResult"
        elif route=="/api/v1/auth/company-check":name="CompanyCheck"
        elif route=="/api/v1/auth/verify-email":name="UserResponse"
        elif route=="/api/v1/auth/resend-verification":name="ResendVerificationResult"
        elif route=="/api/v1/companies/current":name="Company"
        elif route=="/api/v1/companies/current/profile":name="Profile"
        elif route.startswith("/api/v1/news/submissions"):name="Submission"
        elif route=="/api/v1/news":name="ArticlePage"
        elif route.startswith("/api/v1/news/"):name="Article"
        elif route=="/api/v1/events/prepared":name="LLMPreparedPage"
        elif route.endswith("/prepared"):name="LLMPrepared"
        elif route=="/api/v1/insights":name="LLMJob" if method=="POST" else "LLMInsightPage"
        elif route.startswith("/api/v1/insights/"):name="LLMJob"
        elif route.startswith("/api/v1/chat/messages/") or route.endswith("/messages"):name="LLMJob"
        elif route.startswith("/api/v1/chat/threads"):name="LLMThread"
        elif route.startswith("/api/v1/ai/workflows/"):name="LLMWorkflowTrace"
        elif route.startswith("/api/v1/ai/runs/"):name="LLMTrace"
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


def registration_flow(base, operator):
    employee = Client(base)
    company = {"inn":"9900000017","name":"Тест Металл Альфа"}
    checked = employee.request("POST","/api/v1/auth/company-check",company)
    assert checked["status"] == "matched_mock" and checked["provider"] == "mock-registry-v1"
    assert employee.request("POST","/api/v1/auth/company-check",dict(company,inn="9900000018"),expected=400)["code"] == "invalid_inn"
    assert employee.request("POST","/api/v1/auth/company-check",dict(company,name="Чужая компания"),expected=422)["code"] == "company_name_mismatch"
    assert employee.request("POST","/api/v1/auth/company-check",dict(company,inn="9900000056"),expected=422)["code"] == "company_not_found"
    payload = {"full_name":"Иван Тестовый","job_title":"Финансовый директор","email":"employee-"+uuid.uuid4().hex+"@example.test","password":"Registration-Test-2026!","company_name":company["name"],"inn":company["inn"]}
    key = {"Idempotency-Key":uuid.uuid4().hex}
    employee.request("POST","/api/v1/auth/register",dict(payload,region="Москва"),key,400)
    created = employee.request("POST","/api/v1/auth/register",payload,key,201)
    assert created["user"]["access_status"] == "pending_email" and not created["user"]["email_verified"]
    assert "token" not in created and "password" not in created["user"]
    user_id = created["user"]["id"]
    replay = employee.request("POST","/api/v1/auth/register",payload,key)
    assert replay["user"]["id"] == user_id
    employee.request("POST","/api/v1/auth/register",dict(payload,job_title="Главный бухгалтер"),key,409)
    employee.login(payload["email"],payload["password"])
    employee.request("GET","/api/v1/companies/current",expected=403)
    employee.request("GET","/api/v1/news",expected=403)
    old_token = operator(["mock-email","--email",payload["email"]])["token"]
    employee.request("POST","/api/v1/auth/resend-verification",{"email":payload["email"]},expected=202)
    employee.request("POST","/api/v1/auth/verify-email",{"token":old_token},expected=400)
    token = operator(["mock-email","--email",payload["email"]])["token"]
    verified = employee.request("POST","/api/v1/auth/verify-email",{"token":token})
    assert verified["user"]["access_status"] == "pending_approval"
    employee.request("POST","/api/v1/auth/verify-email",{"token":token},expected=400)
    employee.request("GET","/api/v1/companies/current",expected=403)
    operator(["approve-registration","--user-id",user_id,"--operator","e2e-operator"])
    current = employee.request("GET","/api/v1/auth/me")
    assert current["user"]["access_status"] == "active" and current["user"]["job_title"] == payload["job_title"]
    current_company = employee.request("GET","/api/v1/companies/current")
    assert current_company["inn"] == company["inn"] and current_company["region"] == "" and current_company["sector"] == "" and "revenue_minor" not in current_company and current_company["okved"] is None
    employee.request("GET","/api/v1/companies/current/profile",expected=404)
    employee.request("GET","/api/v1/companies/current?company_id=company-metal",expected=403)
    operator(["revoke-access","--user-id",user_id,"--operator","e2e-operator"])
    employee.request("GET","/api/v1/auth/me",expected=401)
    employee.request("POST","/api/v1/auth/login",{"email":payload["email"],"password":payload["password"]},expected=403)


def llm_flow(metal, other):
    prepared = eventually(lambda: metal.request("GET", "/api/v1/events/prepared?limit=100"),
                          lambda p: len(p["items"]) == 6, "LLM-tagged fixture events")
    event = prepared["items"][0]
    assert event["synthetic"] and event["provider_version"] == "mock-v1"
    key = {"Idempotency-Key": uuid.uuid4().hex}
    card = metal.request("POST", "/api/v1/insights", {"event_id": event["event"]["event_id"]}, key, 202)
    again = metal.request("POST", "/api/v1/insights", {"event_id": event["event"]["event_id"]}, key, 202)
    assert card["id"] == again["id"]
    ready = eventually(lambda: metal.request("GET", "/api/v1/insights/" + card["id"]),
                       lambda j: j["status"] == "ready", "generated insight")
    assert ready["result"]["synthetic"] and ready["result"]["check"]["passed"]
    result = ready["result"]
    assert result["workflow_id"] and result["profile_run_id"] and result["business_profile"]["company_id"] == "company-metal"
    workflow = metal.request("GET", "/api/v1/ai/workflows/" + result["workflow_id"])
    assert workflow["status"] == "ready" and {s["name"] for s in workflow["steps"]} == {"profile", "analysis", "check"}
    assert {s["model"] for s in workflow["steps"]} == {"deepseek/deepseek-v4.1-flash", "qwen/qwen3.6-35b-a3b", "google/gemma-4-26b-a4b-it"}
    other.request("GET", "/api/v1/ai/workflows/" + result["workflow_id"], expected=404)
    second_event = prepared["items"][1]["event"]["event_id"]
    second = metal.request("POST", "/api/v1/insights", {"event_id": second_event}, {"Idempotency-Key": uuid.uuid4().hex}, 202)
    second_ready = eventually(lambda: metal.request("GET", "/api/v1/insights/" + second["id"]), lambda j: j["status"] == "ready", "cached company interpretation")
    assert second_ready["result"]["profile_run_id"] == result["profile_run_id"]
    other.request("GET", "/api/v1/insights/" + card["id"], expected=404)
    trace = metal.request("GET", "/api/v1/ai/runs/" + ready["result"]["run_id"])
    assert trace["synthetic"] and trace["status"] == "ready"
    other.request("GET", "/api/v1/ai/runs/" + trace["run_id"], expected=404)
    thread = metal.request("POST", "/api/v1/chat/threads", {"insight_id": card["id"]},
                           {"Idempotency-Key": uuid.uuid4().hex}, 202)
    other.request("GET", "/api/v1/chat/threads/" + thread["id"], expected=404)
    message_key = {"Idempotency-Key": uuid.uuid4().hex}
    message = metal.request("POST", "/api/v1/chat/threads/" + thread["id"] + "/messages",
                            {"message": "Что произошло в новости?"}, message_key, 202)
    repeated = metal.request("POST", "/api/v1/chat/threads/" + thread["id"] + "/messages",
                             {"message": "Что произошло в новости?"}, message_key, 202)
    assert message["id"] == repeated["id"]
    metal.request("POST", "/api/v1/chat/threads/" + thread["id"] + "/messages",
                  {"message": "Другой вопрос"}, message_key, 409)
    answer = eventually(lambda: metal.request("GET", "/api/v1/chat/messages/" + message["id"]),
                        lambda j: j["status"] == "ready", "chat answer")
    assert answer["result"]["answer"]["sources"] and answer["result"]["synthetic"]
    other.request("GET", "/api/v1/chat/messages/" + message["id"], expected=404)
    history = metal.request("GET", "/api/v1/chat/threads/" + thread["id"])
    assert len(history["messages"]) == 1
    print("LLM mock: event -> insight -> chat, idempotency and company isolation passed")


def run(base, password, collect, operator=None):
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
    assert all(item["status"] in {"awaiting_tagging", "tagged"} for item in public_events)
    llm_flow(metal, build)
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
    if operator:
        registration_flow(base, operator)
    report = {"result": "passed", "public_articles": 7, "candidate_events": 6, "exact_duplicate_groups": 6, "manual_privacy": "passed", "updates_and_idempotency": "passed", "source_mode": "fixture"}
    if operator:
        report["registration_and_membership"]="passed"
    print(json.dumps(report, ensure_ascii=False))
    return report


def compose_collect(updated):
    command = ["docker", "compose", "--env-file", ".env", "-f", "infrastructure/docker/compose.yaml", "exec", "-T"]
    if updated:
        command += ["-e", "SOURCE_CONFIG=/fixtures/sources-updated.json"]
    command += ["news-service", "/app", "collect"]
    subprocess.run(command, cwd=ROOT, check=True, timeout=60)


def compose_operator(args):
    command=["docker","compose","--env-file",".env","-f","infrastructure/docker/compose.yaml","exec","-T","api-gateway","/app",*args]
    result=subprocess.run(command,cwd=ROOT,check=True,capture_output=True,text=True,timeout=15)
    return json.loads(result.stdout)


if __name__ == "__main__":
    run(os.environ.get("E2E_BASE_URL", "http://localhost:8080"), os.environ.get("DEMO_PASSWORD", "DemoOnly-2026!"), compose_collect, compose_operator)
