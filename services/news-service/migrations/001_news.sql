CREATE TABLE IF NOT EXISTS news.sources (
 id text PRIMARY KEY, config jsonb NOT NULL, priority integer NOT NULL, enabled boolean NOT NULL,
 next_run_at timestamptz NOT NULL DEFAULT now(), lease_until timestamptz, lease_owner text,
 last_success_at timestamptz, last_error text, consecutive_errors integer NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS news.host_leases(host text PRIMARY KEY,owner text NOT NULL,lease_until timestamptz NOT NULL);
CREATE TABLE IF NOT EXISTS news.runs (
 id text PRIMARY KEY, source_id text NOT NULL, status text NOT NULL, correlation_id text NOT NULL,
 started_at timestamptz NOT NULL DEFAULT now(), finished_at timestamptz, discovered integer NOT NULL DEFAULT 0, error_code text
);
CREATE TABLE IF NOT EXISTS news.fetch_cache (
 source_id text NOT NULL, url text NOT NULL, etag text NOT NULL, modified text NOT NULL,
 body bytea NOT NULL DEFAULT '', validated_at timestamptz NOT NULL DEFAULT now(), PRIMARY KEY(source_id,url)
);
CREATE TABLE IF NOT EXISTS news.raw_responses (
 id text PRIMARY KEY, scope text NOT NULL, source_id text NOT NULL, url text NOT NULL,
 body bytea NOT NULL CHECK(octet_length(body)<=2097152), created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS news.submissions (
 id text PRIMARY KEY, company_id text NOT NULL, user_id text NOT NULL, idempotency_key text NOT NULL,
 request_hash text NOT NULL, status text NOT NULL, article_id text, error_code text, created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(company_id,user_id,idempotency_key)
);
CREATE TABLE IF NOT EXISTS news.jobs (
 id text PRIMARY KEY, source_config jsonb NOT NULL, candidate jsonb NOT NULL, scope text NOT NULL,
 origin text NOT NULL, run_id text, submission_id text REFERENCES news.submissions(id),
 fingerprint text NOT NULL, correlation_id text NOT NULL, status text NOT NULL DEFAULT 'pending',
 attempts integer NOT NULL DEFAULT 0, next_attempt_at timestamptz NOT NULL DEFAULT now(), lease_until timestamptz, lease_owner text,
 error_code text, created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(), UNIQUE(run_id,fingerprint)
);
CREATE INDEX IF NOT EXISTS jobs_due ON news.jobs(next_attempt_at) WHERE status IN ('pending','running','retry');
CREATE TABLE IF NOT EXISTS news.articles (
 id text PRIMARY KEY, source_id text NOT NULL, scope text NOT NULL, external_id text NOT NULL, url text NOT NULL,
 revision integer NOT NULL CHECK(revision>0), revision_hash text NOT NULL, document jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(), UNIQUE(source_id,scope,external_id)
);
CREATE UNIQUE INDEX IF NOT EXISTS articles_url ON news.articles(source_id,scope,url) WHERE url<>'';
CREATE INDEX IF NOT EXISTS articles_scope ON news.articles(scope,created_at DESC,id);
CREATE TABLE IF NOT EXISTS news.article_revisions (
 article_id text NOT NULL REFERENCES news.articles(id), revision integer NOT NULL, document jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(), PRIMARY KEY(article_id,revision)
);
