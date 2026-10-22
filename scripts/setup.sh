#!/bin/sh
set -eu
PROJECT_ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$PROJECT_ROOT"
if [ -e .env ]; then
  echo '.env already exists; preserved.'
  exit 0
fi
umask 077
python3 - <<'PY'
from pathlib import Path
import secrets
content=Path('.env.example').read_text()
lines=[]
for line in content.splitlines():
    if '=' in line:
        key,value=line.split('=',1)
        if key.endswith('_DB_PASSWORD') or key in {'POSTGRES_PASSWORD','INTERNAL_KEY'}:
            line=key+'='+secrets.token_hex(32)
    lines.append(line)
with Path('.env').open('x') as stream:
    stream.write('\n'.join(lines)+'\n')
print('Created .env with independent local secrets. Demo password is documented in README.')
PY
