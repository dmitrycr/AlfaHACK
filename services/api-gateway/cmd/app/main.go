package main

import (
	"alfa.local/api-gateway/internal/repository/postgres"
	"alfa.local/api-gateway/internal/service"
	transport "alfa.local/api-gateway/internal/transport/http"
	"alfa.local/api-gateway/migrations"
	"alfa.local/shared/platform"
	"alfa.local/shared/rpc"
	"log/slog"
	"os"
)

func main() {
	ctx := platform.Context()
	p, e := platform.Open(ctx)
	if e != nil {
		slog.Error("database unavailable", "error", e)
		os.Exit(1)
	}
	defer p.Close()
	if e = platform.Migrate(ctx, p, "auth", migrations.SQL); e != nil {
		slog.Error("migration failed", "error", e)
		os.Exit(1)
	}
	store := postgres.Store{Pool: p}
	if platform.Env("SEED_DEMO", "false") == "true" {
		if platform.Env("APP_ENV", "development") == "production" {
			slog.Error("demo seed forbidden in production")
			os.Exit(1)
		}
		if e = store.Seed(ctx, platform.Required("DEMO_PASSWORD")); e != nil {
			slog.Error("seed failed", "error", e)
			os.Exit(1)
		}
	}
	company, e := platform.Dial(platform.Env("COMPANY_ADDR", "company-service:9090"))
	if e != nil {
		panic(e)
	}
	defer company.Close()
	news, e := platform.Dial(platform.Env("NEWS_ADDR", "news-service:9090"))
	if e != nil {
		panic(e)
	}
	defer news.Close()
	events, e := platform.Dial(platform.Env("EVENTS_ADDR", "event-service:9090"))
	if e != nil {
		panic(e)
	}
	defer events.Close()
	analytics, e := platform.Dial(platform.Env("ANALYTICS_ADDR", "analytics-service:9090"))
	if e != nil {
		panic(e)
	}
	defer analytics.Close()
	secure := platform.Env("COOKIE_SECURE", "false") == "true"
	if platform.Env("APP_ENV", "development") == "production" && !secure {
		slog.Error("secure cookies required in production")
		os.Exit(1)
	}
	api := transport.API{Auth: service.NewAuth(store), Company: rpc.NewCompanyServiceClient(company), News: rpc.NewNewsServiceClient(news), Events: rpc.NewEventServiceClient(events), Analytics: rpc.NewAnalyticsServiceClient(analytics), Key: platform.Required("INTERNAL_KEY"), Origin: platform.Env("APP_ORIGIN", "http://localhost:8080"), Secure: secure}
	if e = platform.Serve(ctx, "api-gateway", p, nil, api.Handler()); e != nil {
		slog.Error("service failed", "error", e)
		os.Exit(1)
	}
}
