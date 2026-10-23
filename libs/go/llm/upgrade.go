package llm

// Separate from Tables: never change the checksum of an applied v1 migration.
const UpgradeTables = `
ALTER TABLE agent.calls ADD COLUMN IF NOT EXISTS reserved_nano bigint NOT NULL DEFAULT 0;
ALTER TABLE agent.calls ADD COLUMN IF NOT EXISTS cost_nano bigint NOT NULL DEFAULT 0;
ALTER TABLE agent.calls ADD COLUMN IF NOT EXISTS reasoning_tokens integer NOT NULL DEFAULT 0;
ALTER TABLE agent.calls ADD COLUMN IF NOT EXISTS terminal boolean NOT NULL DEFAULT false;
ALTER TABLE agent.calls ADD COLUMN IF NOT EXISTS finished_at timestamptz;
ALTER TABLE agent.calls ADD COLUMN IF NOT EXISTS provider_version text NOT NULL DEFAULT '';
ALTER TABLE agent.calls ADD COLUMN IF NOT EXISTS usage_known boolean NOT NULL DEFAULT false;
UPDATE agent.calls SET reserved_nano=reserved_minor*10000000,cost_nano=cost_minor*10000000 WHERE reserved_nano=0 AND reserved_minor>0;
CREATE TABLE IF NOT EXISTS agent.controls(day date PRIMARY KEY,reason text NOT NULL);
ALTER TABLE agent.controls ADD COLUMN IF NOT EXISTS provider_version text NOT NULL DEFAULT '';
CREATE TABLE IF NOT EXISTS agent.workflows (
 id text PRIMARY KEY,workflow_key text UNIQUE NOT NULL,scope text NOT NULL,task text NOT NULL,
 status text NOT NULL DEFAULT 'pending',lease text NOT NULL DEFAULT '',lease_until timestamptz,
 attempts integer NOT NULL DEFAULT 0,result jsonb,error_code text NOT NULL DEFAULT '',
 created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now());
ALTER TABLE agent.workflows ADD COLUMN IF NOT EXISTS entity_id text NOT NULL DEFAULT '';
CREATE TABLE IF NOT EXISTS agent.steps (
 workflow_id text NOT NULL REFERENCES agent.workflows(id),name text NOT NULL,
 status text NOT NULL,result jsonb,run_id text NOT NULL DEFAULT '',error_code text NOT NULL DEFAULT '',
 updated_at timestamptz NOT NULL DEFAULT now(),PRIMARY KEY(workflow_id,name));
`
