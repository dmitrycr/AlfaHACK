import grpc
import pytest

from ml.company_client import CompanyClient, CompanyServiceError


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
