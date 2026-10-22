package main

import (
	"alfa.local/event-service/internal/service"
	transport "alfa.local/event-service/internal/transport/grpc"
	"alfa.local/event-service/migrations"
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
	if e = platform.Migrate(ctx, p, "events", migrations.SQL+bus.Tables("events")); e != nil {
		slog.Error("migration failed", "error", e)
		os.Exit(1)
	}
	consumer, e := bus.NewKafka(platform.Required("KAFKA_BROKERS"), "event-service-v1", bus.Articles)
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
	go func() { defer wg.Done(); consumer.Run(worker, p, "events", service.HandleArticle) }()
	go func() { defer wg.Done(); bus.RunPublisher(worker, p, "events", producer) }()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		text, e := bus.MetricsText(r.Context(), p, "events", "event-service")
		if e != nil {
			http.Error(w, "metrics unavailable", 503)
			return
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		w.Write([]byte(text))
	})
	e = platform.Serve(ctx, "event-service", p, func(s *grpc.Server) {
		rpc.RegisterEventServiceServer(s, &transport.Server{Store: service.Store{Pool: p}})
	}, mux)
	cancel()
	wg.Wait()
	if e != nil {
		slog.Error("service failed", "error", e)
		os.Exit(1)
	}
}
