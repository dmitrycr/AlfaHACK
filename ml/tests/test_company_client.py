import json
from concurrent import futures

import grpc
import pytest

from ml.company_client import CompanyClient, CompanyServiceError
from ml.grpc_gen import backend_pb2, backend_pb2_grpc


class FakeCompany(backend_pb2_grpc.CompanyServiceServicer):
    """Заглушка Company Service в памяти: версия профиля должна быть last+1."""

    def __init__(self):
        self.profiles: dict[str, list[dict]] = {}
        self.seen_metadata: list = []

    def GetCompany(self, request, context):
        return backend_pb2.Response(payload=json.dumps({"id": request.company_id}).encode())

    def GetProfile(self, request, context):
        self.seen_metadata.append(dict(context.invocation_metadata()))
        if not self.profiles.get(request.company_id):
            context.abort(grpc.StatusCode.NOT_FOUND, "profile not found")
        return backend_pb2.Response(payload=json.dumps(self.profiles[request.company_id][-1]).encode())

    def ReplaceProfile(self, request, context):
        p = json.loads(request.payload.decode("utf-8"))
        hist = self.profiles.setdefault(request.company_id, [])
        if p["company_id"] != request.company_id:
            context.abort(grpc.StatusCode.PERMISSION_DENIED, "company mismatch")
        if p["version"] != len(hist) + 1:
            context.abort(grpc.StatusCode.ABORTED, "profile version conflict")
        hist.append(p)
        return backend_pb2.Response(payload=request.payload)


@pytest.fixture
def fake():
    svc = FakeCompany()
    server = grpc.server(futures.ThreadPoolExecutor(max_workers=2))
    backend_pb2_grpc.add_CompanyServiceServicer_to_server(svc, server)
    port = server.add_insecure_port("127.0.0.1:0")
    server.start()
    yield svc, f"127.0.0.1:{port}"
    server.stop(None)


def test_roundtrip_and_version(fake):
    svc, addr = fake
    c = CompanyClient(addr)
    assert c.get_company("U1") == {"id": "U1"}
    with pytest.raises(CompanyServiceError) as e:
        c.get_profile("U1")
    assert e.value.code == grpc.StatusCode.NOT_FOUND
    c.replace_profile("U1", {"channels": [], "ключ": "значение"}, 1)
    assert c.get_profile("U1")["ключ"] == "значение"
    with pytest.raises(CompanyServiceError) as e:
        c.replace_profile("U1", {"channels": []}, 1)          # повтор той же версии
    assert e.value.code == grpc.StatusCode.ABORTED
    assert c.replace_profile("U1", {"channels": []}, 2)["version"] == 2
    c.close()


def test_signed_context_only_with_header(fake):
    svc, addr = fake
    svc.profiles["U1"] = [{"version": 1}]
    CompanyClient(addr, signed_context="SECRET", signed_context_header="").get_profile("U1")
    CompanyClient(addr, signed_context="SECRET", signed_context_header="x-ctx").get_profile("U1")
    assert "x-ctx" not in svc.seen_metadata[0] and svc.seen_metadata[1]["x-ctx"] == "SECRET"


def test_address_from_env_and_default(monkeypatch):
    monkeypatch.delenv("COMPANY_SERVICE_ADDR", raising=False)
    assert CompanyClient().address == "company-service:9090"
    monkeypatch.setenv("COMPANY_SERVICE_ADDR", "h:1")
    assert CompanyClient().address == "h:1"


def test_error_text_has_no_secret(fake):
    _, addr = fake
    with pytest.raises(CompanyServiceError) as e:
        CompanyClient(addr, signed_context="SECRET", signed_context_header="x-ctx").get_profile("none")
    assert "SECRET" not in str(e.value)
