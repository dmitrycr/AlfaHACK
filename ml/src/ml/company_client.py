"""gRPC-клиент Company Service (alfa.v1.CompanyService: GetCompany, GetProfile, ReplaceProfile).
Запрос: Request.company_id; ответ: Response.payload - JSON в UTF-8. Адрес и «подписанный контекст» - из конфига/окружения.
Значение подписанного контекста и ключи в логи не пишутся."""
from __future__ import annotations

import json
import logging
import os

import grpc

from .config import get_config
from .grpc_gen import backend_pb2, backend_pb2_grpc

log = logging.getLogger(__name__)


class CompanyServiceError(Exception):
    """Ошибка вызова: code - grpc.StatusCode, details - текст сервера (без метаданных запроса)."""

    def __init__(self, method: str, code: grpc.StatusCode, details: str):
        super().__init__(f"{method}: {code.name}: {details}")
        self.method, self.code, self.details = method, code, details


class CompanyClient:
    def __init__(self, address: str | None = None, *, channel: grpc.Channel | None = None, timeout_s: float | None = None,
                 signed_context: str | None = None, signed_context_header: str | None = None):
        cfg = get_config().get("company_service", {})
        self.address = address or os.environ.get("COMPANY_SERVICE_ADDR") or cfg.get("address", "company-service:9090")
        self.timeout_s = timeout_s if timeout_s is not None else float(cfg.get("timeout_s", 10))
        # TODO(align): что такое «подписанный контекст», как получить и в каком заголовке слать - бэкенд не назвал; значения не придумываем
        self._signed = signed_context if signed_context is not None else os.environ.get("COMPANY_SIGNED_CONTEXT", "")
        self._header = (signed_context_header if signed_context_header is not None
                        else cfg.get("signed_context_header", "")).lower()
        # TODO(align): TLS не согласован, канал без шифрования
        self._channel = channel or grpc.insecure_channel(self.address)
        self._stub = backend_pb2_grpc.CompanyServiceStub(self._channel)

    def close(self) -> None:
        self._channel.close()

    def _metadata(self) -> tuple | None:
        return ((self._header, self._signed),) if self._signed and self._header else None

    def _call(self, method: str, company_id: str, payload: bytes = b"") -> dict:
        req = backend_pb2.Request(company_id=company_id, payload=payload)
        log.debug("CompanyService.%s company_id=%s", method, company_id)
        try:
            resp = getattr(self._stub, method)(req, timeout=self.timeout_s, metadata=self._metadata())
        except grpc.RpcError as e:
            raise CompanyServiceError(method, e.code(), e.details() or "") from None
        return json.loads(resp.payload.decode("utf-8")) if resp.payload else {}

    def get_company(self, company_id: str) -> dict:
        return self._call("GetCompany", company_id)

    def get_profile(self, company_id: str) -> dict:
        return self._call("GetProfile", company_id)

    def replace_profile(self, company_id: str, profile: dict, version: int) -> dict:
        """Версия должна быть last+1 (сначала get_profile); company_id в профиле = company_id запроса."""
        doc = {**profile, "company_id": company_id, "version": version}
        return self._call("ReplaceProfile", company_id, json.dumps(doc, ensure_ascii=False).encode("utf-8"))
