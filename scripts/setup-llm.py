#!/usr/bin/env python3
"""Add missing LLM settings. --database adds schemas to an existing Compose DB."""
from pathlib import Path
import os
import secrets
import subprocess
import sys

root = Path(__file__).resolve().parents[1]
path = root / '.env'
if not path.exists():
    subprocess.run([str(root / 'scripts/setup.sh')], cwd=root, check=True)
content = path.read_text()
values = dict(line.split('=', 1) for line in content.splitlines() if '=' in line and not line.lstrip().startswith('#'))
defaults = {'AGENT_DB_PASSWORD': secrets.token_hex(32), 'INSIGHTS_DB_PASSWORD': secrets.token_hex(32),
            'LLM_MODE': 'mock', 'LLM_MOCK_SCENARIO': 'success', 'LLM_DAILY_LIMIT_MINOR': '50000',
            'LLM_MAX_PARALLEL': '4', 'LLM_TIMEOUT_SECONDS': '3', 'MATCH_MODE': 'mock'}
# Add only missing LLM settings from the public template; never print values.
for line in (root / '.env.example').read_text().splitlines():
    if line.startswith('LLM_') and '=' in line:
        name, value = line.split('=', 1)
        defaults.setdefault(name, value)
missing = {k: v for k, v in defaults.items() if k not in values}
if missing:
    with path.open('a') as stream:
        stream.write('\n# Backend LLM mock pipeline\n' + ''.join(k + '=' + v + '\n' for k, v in missing.items()))
    path.chmod(0o600)
values.update(missing)
print('LLM configuration ready; existing values preserved.')
if '--database' in sys.argv:
    sql = r'''
\getenv agent_password AGENT_DB_PASSWORD
\getenv insights_password INSIGHTS_DB_PASSWORD
SELECT format('CREATE ROLE app_agent LOGIN PASSWORD %L', :'agent_password') WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname='app_agent') \gexec
SELECT format('CREATE ROLE app_insights LOGIN PASSWORD %L', :'insights_password') WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname='app_insights') \gexec
CREATE SCHEMA IF NOT EXISTS agent AUTHORIZATION app_agent;
CREATE SCHEMA IF NOT EXISTS insights AUTHORIZATION app_insights;
REVOKE ALL ON SCHEMA agent,insights FROM PUBLIC;
'''
    subprocess.run(['docker', 'compose', '--env-file', '.env', '-f', 'infrastructure/docker/compose.yaml',
                    'exec', '-T', '-e', 'AGENT_DB_PASSWORD', '-e', 'INSIGHTS_DB_PASSWORD', 'postgres',
                    'psql', '-X', '-v', 'ON_ERROR_STOP=1', '-U', 'postgres', '-d', 'alfa'],
                   input=sql, text=True, cwd=root, env=dict(os.environ, **values), check=True)
    print('LLM schemas ready; existing roles and passwords preserved.')
