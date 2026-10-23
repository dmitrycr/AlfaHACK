CREATE TABLE IF NOT EXISTS agent.threads (
 id text PRIMARY KEY,company_id text NOT NULL,user_id text NOT NULL,insight_id text NOT NULL,
 idempotency_key text NOT NULL,context jsonb NOT NULL,insight jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),UNIQUE(company_id,user_id,idempotency_key));
