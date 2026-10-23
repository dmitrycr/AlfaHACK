"""Генерация Python-стабов gRPC из proto/backend.proto (копия contracts/proto/v1/backend.proto, не менять) в src/ml/grpc_gen/.
Использование: python scripts/gen_proto.py"""
import _bootstrap  # noqa: F401
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
OUT = ROOT / "src" / "ml" / "grpc_gen"


def main() -> None:
    OUT.mkdir(parents=True, exist_ok=True)
    subprocess.run([sys.executable, "-m", "grpc_tools.protoc", f"-I{ROOT / 'proto'}", f"--python_out={OUT}",
                    f"--grpc_python_out={OUT}", str(ROOT / "proto" / "backend.proto")], check=True)
    grpc_py = OUT / "backend_pb2_grpc.py"
    src = grpc_py.read_text(encoding="utf-8")
    grpc_py.write_text(src.replace("import backend_pb2 as", "from . import backend_pb2 as"), encoding="utf-8")   # пакетный импорт
    (OUT / "__init__.py").write_text('"""Сгенерировано scripts/gen_proto.py из proto/backend.proto. Руками не править."""\n', encoding="utf-8")


if __name__ == "__main__":
    main()
