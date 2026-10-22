package bus

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

func MetricsText(ctx context.Context, p *pgxpool.Pool, schema, service string) (string, error) {
	var pending, inbox, dead int64
	var age float64
	e := p.QueryRow(ctx, "SELECT COUNT(*),COALESCE(EXTRACT(EPOCH FROM now()-MIN(created_at)),0) FROM "+safeSchema(schema)+".outbox WHERE sent_at IS NULL").Scan(&pending, &age)
	if e != nil {
		return "", e
	}
	if e = p.QueryRow(ctx, "SELECT COUNT(*) FROM "+schema+".inbox").Scan(&inbox); e != nil {
		return "", e
	}
	if e = p.QueryRow(ctx, "SELECT COUNT(*) FROM "+schema+".dead_letters").Scan(&dead); e != nil {
		return "", e
	}
	var b strings.Builder
	fmt.Fprintf(&b, "alfa_outbox_pending{service=%q} %d\nalfa_outbox_oldest_seconds{service=%q} %g\nalfa_inbox_messages_total{service=%q} %d\nalfa_dead_letters_total{service=%q} %d\n", service, pending, service, age, service, inbox, service, dead)
	return b.String(), nil
}
