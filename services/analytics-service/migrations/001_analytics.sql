CREATE TABLE IF NOT EXISTS analytics.counters (
 scope text NOT NULL, kind text NOT NULL, source_id text NOT NULL DEFAULT '',
 count bigint NOT NULL DEFAULT 0, latency_seconds_sum double precision NOT NULL DEFAULT 0,
 last_seen_at timestamptz NOT NULL, PRIMARY KEY(scope,kind,source_id)
);
