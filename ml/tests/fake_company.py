"""Заглушка Company Service (gRPC-сервер в памяти) для тестов. Реальный сервис в тестах не вызывается."""
import json
from concurrent import futures

import grpc
import pytest

from ml.grpc_gen import backend_pb2, backend_pb2_grpc


class FakeCompany(backend_pb2_grpc.CompanyServiceServicer):
    """Заглушка Company Service в памяти: версия профиля должна быть last+1."""

    def __init__(self):
        self.profiles: dict[str, list[dict]] = {}
        self.seen_metadata: list = []
        self.fail_for: set[str] = set()            # company_id, для которых ReplaceProfile отвечает UNAVAILABLE
        self.replace_calls = 0

    def GetCompany(self, request, context):
        return backend_pb2.Response(payload=json.dumps({"id": request.company_id}).encode())

    def GetProfile(self, request, context):
        self.seen_metadata.append(dict(context.invocation_metadata()))
        if not self.profiles.get(request.company_id):
            context.abort(grpc.StatusCode.NOT_FOUND, "profile not found")
        return backend_pb2.Response(payload=json.dumps(self.profiles[request.company_id][-1]).encode())

    def ReplaceProfile(self, request, context):
        self.replace_calls += 1
        if request.company_id in self.fail_for:
            context.abort(grpc.StatusCode.UNAVAILABLE, "storage down")
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
