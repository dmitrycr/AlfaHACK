package transport

import (
	"alfa.local/shared/bus"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/http"
	"strings"
)

func Metrics(p *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		base, e := bus.MetricsText(r.Context(), p, "news", "news-service")
		if e != nil {
			http.Error(w, "metrics unavailable", 503)
			return
		}
		var b strings.Builder
		b.WriteString(base)
		rows, e := p.Query(r.Context(), `SELECT id,consecutive_errors,COALESCE(EXTRACT(EPOCH FROM now()-last_success_at),-1) FROM news.sources ORDER BY id`)
		if e != nil {
			http.Error(w, "metrics unavailable", 503)
			return
		}
		for rows.Next() {
			var id string
			var errors int
			var age float64
			if e = rows.Scan(&id, &errors, &age); e != nil {
				break
			}
			fmt.Fprintf(&b, "alfa_source_consecutive_errors{source=%q} %d\nalfa_source_last_success_age_seconds{source=%q} %g\n", id, errors, id, age)
		}
		if e == nil {
			e = rows.Err()
		}
		rows.Close()
		if e != nil {
			http.Error(w, "metrics unavailable", 503)
			return
		}
		rows, e = p.Query(r.Context(), `SELECT source_config->>'id',status,COUNT(*),COALESCE(SUM(EXTRACT(EPOCH FROM updated_at-created_at)),0) FROM news.jobs GROUP BY 1,2`)
		if e != nil {
			http.Error(w, "metrics unavailable", 503)
			return
		}
		for rows.Next() {
			var id, state string
			var count int64
			var duration float64
			if e = rows.Scan(&id, &state, &count, &duration); e != nil {
				break
			}
			fmt.Fprintf(&b, "alfa_news_jobs{source=%q,status=%q} %d\nalfa_news_job_elapsed_seconds_sum{source=%q,status=%q} %g\n", id, state, count, id, state, duration)
		}
		if e == nil {
			e = rows.Err()
		}
		rows.Close()
		if e != nil {
			http.Error(w, "metrics unavailable", 503)
			return
		}
		rows, e = p.Query(r.Context(), `SELECT source_id,status,COUNT(*),COALESCE(SUM(EXTRACT(EPOCH FROM finished_at-started_at)),0) FROM news.runs GROUP BY 1,2`)
		if e != nil {
			http.Error(w, "metrics unavailable", 503)
			return
		}
		for rows.Next() {
			var id, state string
			var count int64
			var duration float64
			if e = rows.Scan(&id, &state, &count, &duration); e != nil {
				break
			}
			fmt.Fprintf(&b, "alfa_collection_runs{source=%q,status=%q} %d\nalfa_collection_duration_seconds_sum{source=%q,status=%q} %g\n", id, state, count, id, state, duration)
		}
		if e == nil {
			e = rows.Err()
		}
		rows.Close()
		if e != nil {
			http.Error(w, "metrics unavailable", 503)
			return
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		w.Write([]byte(b.String()))
	}
}
