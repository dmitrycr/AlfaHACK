CREATE TABLE IF NOT EXISTS events.events (
 id text PRIMARY KEY, scope text NOT NULL, duplicate_group text NOT NULL,
 version integer NOT NULL CHECK(version>0), document jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(), UNIQUE(scope,duplicate_group)
);
CREATE TABLE IF NOT EXISTS events.event_articles (
 event_id text NOT NULL REFERENCES events.events(id), article_id text NOT NULL,
 revision integer NOT NULL, document jsonb NOT NULL, PRIMARY KEY(event_id,article_id)
);
CREATE INDEX IF NOT EXISTS events_article_lookup ON events.event_articles(article_id);
CREATE TABLE IF NOT EXISTS events.article_heads (article_id text PRIMARY KEY, revision integer NOT NULL);
CREATE TABLE IF NOT EXISTS events.event_revisions (
 event_id text NOT NULL REFERENCES events.events(id), version integer NOT NULL, document jsonb NOT NULL,
 PRIMARY KEY(event_id,version)
);
