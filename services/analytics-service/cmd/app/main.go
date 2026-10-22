package main

import (
	"alfa.local/analytics-service/internal/service"
	transport "alfa.local/analytics-service/internal/transport/grpc"
	"alfa.local/analytics-service/migrations"
	"alfa.local/shared/bus"
	"alfa.local/shared/platform"
	"alfa.local/shared/rpc"
	"context"
	"google.golang.org/grpc"
	"log/slog"
	"net/http"
	"os"
	"sync"
)

func main() {
	ctx := platform.Context()
	p, e := platform.Open(ctx)
	if e != nil {
		slog.Error("database unavailable", "error", e)
		os.Exit(1)
	}
	defer p.Close()
	if e = platform.Migrate(ctx, p, "analytics", migrations.SQL+bus.Tables("analytics")); e != nil {
		slog.Error("migration failed", "error", e)
		os.Exit(1)
	}
	consumer, e := bus.NewKafka(platform.Required("KAFKA_BROKERS"), "analytics-service-v1", bus.Articles, bus.Ingestion, bus.Events)
	if e != nil {
		slog.Error("Kafka configuration failed", "error", e)
		os.Exit(1)
	}
	defer consumer.Client.Close()
	producer, e := bus.NewKafka(platform.Required("KAFKA_BROKERS"), "")
	if e != nil {
		slog.Error("Kafka configuration failed", "error", e)
		os.Exit(1)
	}
	defer producer.Client.Close()
	worker, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); consumer.Run(worker, p, "analytics", service.Handle) }()
	go func() { defer wg.Done(); bus.RunPublisher(worker, p, "analytics", producer) }()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		text, e := bus.MetricsText(r.Context(), p, "analytics", "analytics-service")
		if e != nil {
			http.Error(w, "metrics unavailable", 503)
			return
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		w.Write([]byte(text))
	})
	e = platform.Serve(ctx, "analytics-service", p, func(s *grpc.Server) {
		rpc.RegisterAnalyticsServiceServer(s, &transport.Server{Store: service.Store{Pool: p}})
	}, mux)
	cancel()
	wg.Wait()
	if e != nil {
		slog.Error("service failed", "error", e)
		os.Exit(1)
	}
}
