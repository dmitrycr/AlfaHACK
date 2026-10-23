CREATE TABLE IF NOT EXISTS insights.matches (
 company_id text NOT NULL,event_id text NOT NULL,profile_version integer NOT NULL,event_version integer NOT NULL,
 document jsonb NOT NULL,created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(company_id,event_id,profile_version,event_version));
