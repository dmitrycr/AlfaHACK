#!/usr/bin/env python3
"""Disposable PostgreSQL database, real service binaries, and kfake Kafka wire mock.

Requires TEST_DATABASE_URL (a local test PostgreSQL admin), Go 1.25 and psql.
Creates/drops only uniquely named databases and roles. Never resets caller data.
"""
import importlib.util
import json
import os
from pathlib import Path
import secrets
import select
import shutil
import signal
import socket
import subprocess
import tempfile
import time
import urllib.parse
import urllib.request

ROOT = Path(__file__).resolve().parents[1]
SERVICES = {"api-gateway": "auth", "company-service": "company", "news-service": "news", "event-service": "events", "analytics-service": "analytics"}


def free_port():
    with socket.socket() as listener:
        listener.bind(("127.0.0.1", 0))
        return listener.getsockname()[1]


def main():
    admin_url = urllib.parse.urlparse(os.environ["TEST_DATABASE_URL"])
    assert admin_url.hostname in {"127.0.0.1", "localhost", "::1"}, "Use a local test PostgreSQL"
    suffix = secrets.token_hex(8)
    database = "alfa_e2e_" + suffix
    pg_env = dict(os.environ, PGHOST=admin_url.hostname, PGPORT=str(admin_url.port or 5432), PGUSER=urllib.parse.unquote(admin_url.username or "postgres"), PGPASSWORD=urllib.parse.unquote(admin_url.password or ""))
    psql = os.environ.get("PSQL", shutil.which("psql") or "psql")

    def sql(statement, db=None):
        env = dict(pg_env, PGDATABASE=db or admin_url.path.lstrip("/") or "postgres")
        result=subprocess.run([psql, "-X", "-q", "-v", "ON_ERROR_STOP=1"], input=statement, text=True, env=env, capture_output=True, timeout=15)
        if result.returncode:
            raise RuntimeError("PostgreSQL test operation failed: "+result.stderr[-1500:])

    roles = {name: ("alfa_" + schema + "_" + suffix, secrets.token_hex(24)) for name, schema in SERVICES.items()}
    sql("CREATE DATABASE " + database)
    processes, logs = [], {}
    try:
        sql("REVOKE CREATE ON SCHEMA public FROM PUBLIC;", database)
        for name, schema in SERVICES.items():
            role, password = roles[name]
            sql(f"CREATE ROLE {role} LOGIN PASSWORD '{password}'; CREATE SCHEMA {schema} AUTHORIZATION {role}; REVOKE ALL ON SCHEMA {schema} FROM PUBLIC;", database)
        with tempfile.TemporaryDirectory(prefix="alfa-e2e-") as temporary:
            work = Path(temporary)
            binaries = {}
            for name in [*SERVICES, "testbroker"]:
                binary = work / name
                module = ROOT / "libs/go" if name == "testbroker" else ROOT / "services" / name
                target = "./cmd/testbroker" if name == "testbroker" else "./cmd/app"
                subprocess.run(["go", "build", "-o", str(binary), target], cwd=module, check=True, timeout=120)
                binaries[name] = binary
            broker = subprocess.Popen([str(binaries["testbroker"])], stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
            processes.append(broker)
            ready, _, _ = select.select([broker.stdout], [], [], 15)
            assert ready, "mock broker did not start"
            brokers = ",".join(json.loads(broker.stdout.readline())["brokers"])
            addresses = {name: (free_port(), free_port()) for name in SERVICES}
            key = secrets.token_hex(32)
            demo_password = "DemoOnly-2026!"
            environments = {}
            for name in ["company-service", "news-service", "event-service", "analytics-service", "api-gateway"]:
                http_port, rpc_port = addresses[name]
                role, password = roles[name]
                dsn = urllib.parse.urlunparse(("postgresql", f"{role}:{password}@{admin_url.hostname}:{admin_url.port or 5432}", "/" + database, "", "sslmode=disable", ""))
                env = dict(os.environ, DATABASE_URL=dsn, INTERNAL_KEY=key, KAFKA_BROKERS=brokers, HTTP_ADDR=f"127.0.0.1:{http_port}", GRPC_ADDR=f"127.0.0.1:{rpc_port}", APP_ENV="test", SEED_DEMO="true", DEMO_PASSWORD=demo_password, SOURCE_MODE="fixture", COOKIE_SECURE="false", APP_ORIGIN=f"http://127.0.0.1:{addresses['api-gateway'][0]}")
                env.pop("TEST_DATABASE_URL",None)
                for variable, target in [("COMPANY_ADDR", "company-service"), ("NEWS_ADDR", "news-service"), ("EVENTS_ADDR", "event-service"), ("ANALYTICS_ADDR", "analytics-service")]:
                    env[variable] = f"127.0.0.1:{addresses[target][1]}"
                environments[name] = env
                log = (work / (name + ".log")).open("w+")
                logs[name] = log
                process = subprocess.Popen([str(binaries[name])], env=env, stdout=log, stderr=log)
                processes.append(process)
                deadline = time.monotonic() + 30
                while time.monotonic() < deadline:
                    if process.poll() is not None:
                        raise AssertionError(name + " exited")
                    try:
                        with urllib.request.urlopen(f"http://127.0.0.1:{http_port}/readyz", timeout=1) as response:
                            if response.status == 200:
                                break
                    except OSError:
                        time.sleep(0.1)
                else:
                    raise AssertionError(name + " readiness timed out")
                with urllib.request.urlopen(f"http://127.0.0.1:{http_port}/metrics",timeout=5) as response:
                    assert response.status==200 and b"alfa_" in response.read(), name+" metrics failed"

            # Check the actual database roles cannot read a neighbouring schema.
            role, password = roles["news-service"]
            isolated_env = dict(pg_env, PGUSER=role, PGPASSWORD=password, PGDATABASE=database)
            result = subprocess.run([psql, "-X", "-q", "-c", "SELECT * FROM company.companies"], env=isolated_env, capture_output=True, text=True)
            assert result.returncode != 0 and "permission denied" in result.stderr, "cross-schema access was permitted"

            spec = importlib.util.spec_from_file_location("alfa_e2e", ROOT / "scripts/e2e.py")
            e2e = importlib.util.module_from_spec(spec)
            spec.loader.exec_module(e2e)

            def collect(updated):
                env = dict(environments["news-service"])
                if updated:
                    env["SOURCE_CONFIG"] = str(ROOT / "services/news-service/internal/source/fixtures/sources-updated.json")
                subprocess.run([str(binaries["news-service"]), "collect"], env=env, stdout=subprocess.DEVNULL, check=True, timeout=60)

            try:
                e2e.run(f"http://127.0.0.1:{addresses['api-gateway'][0]}", demo_password, collect)
            except BaseException:
                for name, log in logs.items():
                    log.flush()
                    log.seek(0)
                    print(name + ":\n" + "\n".join(log.read().splitlines()[-30:]))
                raise
            finally:
                for process in reversed(processes):
                    process.send_signal(signal.SIGTERM)
                    try:
                        process.wait(timeout=10)
                    except subprocess.TimeoutExpired:
                        process.kill()
                        process.wait()
                processes.clear()
                for log in logs.values():
                    log.close()
    finally:
        for process in reversed(processes):
            process.terminate()
            try:
                process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait()
        sql("DROP DATABASE " + database + " WITH (FORCE)")
        for role, _ in roles.values():
            sql("DROP ROLE IF EXISTS " + role)


if __name__ == "__main__":
    main()
