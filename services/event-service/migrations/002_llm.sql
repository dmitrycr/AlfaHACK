CREATE TABLE IF NOT EXISTS events.prepared_events (
 event_id text NOT NULL REFERENCES events.events(id), event_version integer NOT NULL,
 scope text NOT NULL, document jsonb NOT NULL, created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(event_id,event_version));
