package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"alfa.local/news-service/internal/repository/postgres"
	"alfa.local/news-service/internal/service"
	"alfa.local/news-service/internal/source"
	transport "alfa.local/news-service/internal/transport/grpc"
	httptransport "alfa.local/news-service/internal/transport/http"
	"alfa.local/news-service/migrations"
	"alfa.local/shared/bus"
	"alfa.local/shared/platform"
	"alfa.local/shared/rpc"
	"github.com/jackc/pgx/v5"
	"google.golang.org/grpc"
)

func main() {
	ctx := platform.Context()
	if platform.Env("SOURCE_MODE", "fixture") != "fixture" {
		slog.Error("only fixture sources are enabled in this iteration")
		os.Exit(1)
	}
	cfg, e := source.DefaultConfig()
	if file := os.Getenv("SOURCE_CONFIG"); file != "" {
		var b []byte
		b, e = os.ReadFile(file)
		if e == nil {
			cfg, e = source.Load(b)
		}
	}
	if e != nil {
		slog.Error("source configuration invalid", "error", e)
		os.Exit(1)
	}
	pool, e := platform.Open(ctx)
	if e != nil {
		slog.Error("database unavailable", "error", e)
		os.Exit(1)
	}
	defer pool.Close()
	if e = platform.Migrate(ctx, pool, "news", migrations.SQL+bus.Tables("news")); e != nil {
		slog.Error("migration failed", "error", e)
		os.Exit(1)
	}
	store := postgres.Store{Pool: pool}
	if e = store.SyncSources(ctx, cfg); e != nil {
		slog.Error("source sync failed", "error", e)
		os.Exit(1)
	}
	collector := &service.Collector{Store: store, Fetcher: source.FixtureFetcher{FS: source.Fixtures, Resources: cfg.Resources}}
	if len(os.Args) > 1 && os.Args[1] == "collect" {
		for _, src := range cfg.Sources {
			if !src.Enabled {
				continue
			}
			var owner string
			deadline := time.Now().Add(45 * time.Second)
			for {
				owner, e = store.ClaimSpecificSource(ctx, src.ID)
				if e == nil {
					e = collector.Collect(ctx, src, owner)
				}
				if (!errors.Is(e, pgx.ErrNoRows) && !errors.Is(e, postgres.ErrHostBusy)) || time.Now().After(deadline) {
					break
				}
				time.Sleep(100 * time.Millisecond)
			}
			if e != nil {
				slog.Error("source is busy", "source_id", src.ID)
				os.Exit(1)
			}
		}
		for i := 0; i < 1000; i++ {
			job, err := store.ClaimJob(ctx)
			if errors.Is(err, pgx.ErrNoRows) {
				break
			}
			if err == nil {
				err = collector.ProcessJob(ctx, job)
			}
			if err != nil {
				slog.Error("collection failed", "error", err)
				os.Exit(1)
			}
		}
		slog.Info("fixture collection completed")
		return
	}
	kafka, e := bus.NewKafka(platform.Required("KAFKA_BROKERS"), "")
	if e != nil {
		slog.Error("Kafka configuration failed", "error", e)
		os.Exit(1)
	}
	defer kafka.Client.Close()
	workerCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	start := func(f func()) { wg.Add(1); go func() { defer wg.Done(); f() }() }
	start(func() { bus.RunPublisher(workerCtx, pool, "news", kafka) })
	if platform.Env("RUN_SCHEDULER", "true") == "true" {
		for i := 0; i < 4; i++ {
			start(func() { collector.Run(workerCtx) })
		}
	}
	start(func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			if err := store.Cleanup(workerCtx); err != nil {
				slog.Warn("raw retention failed", "error", err)
			}
			select {
			case <-workerCtx.Done():
				return
			case <-ticker.C:
			}
		}
	})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /metrics", httptransport.Metrics(pool))
	e = platform.Serve(ctx, "news-service", pool, func(s *grpc.Server) { rpc.RegisterNewsServiceServer(s, &transport.Server{Store: store, Config: cfg}) }, mux)
	cancel()
	wg.Wait()
	if e != nil {
		slog.Error("service failed", "error", e)
		os.Exit(1)
	}
}
