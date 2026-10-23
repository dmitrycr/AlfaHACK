package service

import (
	"alfa.local/shared/bus"
	"alfa.local/shared/model"
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"time"
)

type Store struct{ Pool *pgxpool.Pool }

func Handle(ctx context.Context, tx pgx.Tx, m model.Envelope) error {
	allowed := map[string]bool{"article.stored": true, "article.retry": true, "article.failed": true, "article.quarantined": true, "ingestion.completed": true, "ingestion.failed": true, "event.prepared": true, "event.tagged": true, "event.excluded": true, "event.tagging_failed": true}
	if !allowed[m.Type] {
		return bus.Permanent{Reason: "unsupported_analytics_message"}
	}
	var meta struct {
		SourceID string `json:"source_id"`
	}
	if json.Unmarshal(m.Payload, &meta) != nil {
		return bus.Permanent{Reason: "invalid_analytics_payload"}
	}
	if len(meta.SourceID) > 100 {
		return bus.Permanent{Reason: "invalid_source_id"}
	}
	latency := time.Since(m.OccurredAt).Seconds()
	if latency < 0 {
		latency = 0
	}
	_, e := tx.Exec(ctx, `INSERT INTO analytics.counters(scope,kind,source_id,count,latency_seconds_sum,last_seen_at) VALUES($1,$2,$3,1,$4,now()) ON CONFLICT(scope,kind,source_id) DO UPDATE SET count=analytics.counters.count+1,latency_seconds_sum=analytics.counters.latency_seconds_sum+excluded.latency_seconds_sum,last_seen_at=now()`, m.Scope, m.Type, meta.SourceID, latency)
	return e
}

type Counter struct {
	Kind       string    `json:"kind"`
	SourceID   string    `json:"source_id"`
	Count      int64     `json:"count"`
	LatencySum float64   `json:"delivery_latency_seconds_sum"`
	LastSeen   time.Time `json:"last_seen_at"`
}

func (s Store) Summary(ctx context.Context, company string) ([]Counter, error) {
	out := []Counter{}
	rows, e := s.Pool.Query(ctx, `SELECT kind,source_id,SUM(count),SUM(latency_seconds_sum),MAX(last_seen_at) FROM analytics.counters WHERE scope='public' OR scope=$1 GROUP BY kind,source_id ORDER BY kind,source_id`, "company:"+company)
	if e != nil {
		return out, e
	}
	defer rows.Close()
	for rows.Next() {
		var c Counter
		if e = rows.Scan(&c.Kind, &c.SourceID, &c.Count, &c.LatencySum, &c.LastSeen); e != nil {
			return out, e
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
