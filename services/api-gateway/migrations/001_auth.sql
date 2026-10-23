CREATE TABLE IF NOT EXISTS auth.users (
 id text PRIMARY KEY, email text NOT NULL UNIQUE, company_id text NOT NULL, password_hash bytea NOT NULL
);
CREATE TABLE IF NOT EXISTS auth.sessions (
 token_hash text PRIMARY KEY, user_id text NOT NULL REFERENCES auth.users(id), csrf_hash text NOT NULL,
 created_at timestamptz NOT NULL, last_seen_at timestamptz NOT NULL, expires_at timestamptz NOT NULL
);
CREATE INDEX IF NOT EXISTS sessions_expiry ON auth.sessions(expires_at);
CREATE TABLE IF NOT EXISTS auth.login_attempts (
 bucket_key text NOT NULL, minute bigint NOT NULL, attempts integer NOT NULL,
 PRIMARY KEY(bucket_key,minute)
);
