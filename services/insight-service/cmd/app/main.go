package main

import (
	"alfa.local/insight-service/internal/service"
	transport "alfa.local/insight-service/internal/transport/grpc"
	"alfa.local/insight-service/migrations"
	"alfa.local/shared/llm"
	"alfa.local/shared/pipeline"
	"alfa.local/shared/platform"
	"alfa.local/shared/rpc"
	"context"
	"google.golang.org/grpc"
	"log/slog"
	"os"
	"sync"
)

func main() {
	ctx := platform.Context()
	p, e := platform.Open(ctx)
	if e != nil {
		slog.Error("database unavailable")
		os.Exit(1)
	}
	defer p.Close()
	if e = migrations.Apply(ctx, p); e != nil {
		slog.Error("migration failed", "error", e)
		os.Exit(1)
	}
	company, e := platform.Dial(platform.Env("COMPANY_ADDR", "company-service:9090"))
	if e != nil {
		panic(e)
	}
	defer company.Close()
	events, e := platform.Dial(platform.Env("EVENTS_ADDR", "event-service:9090"))
	if e != nil {
		panic(e)
	}
	defer events.Close()
	ai, e := platform.Dial(platform.Env("AI_ADDR", "ai-agent-service:9090"))
	if e != nil {
		panic(e)
	}
	defer ai.Close()
	mode := platform.Env("MATCH_MODE", "mock")
	if !llm.Contains([]string{"mock", "external"}, mode) {
		slog.Error("invalid MATCH_MODE")
		os.Exit(1)
	}
	svc := service.Service{Pool: p, Client: pipeline.Client{Key: platform.Required("INTERNAL_KEY"), Subject: "insight-service"}, Company: rpc.NewCompanyServiceClient(company), Events: rpc.NewEventServiceClient(events), AI: rpc.NewAIServiceClient(ai), Knowledge: llm.MockKnowledge{}, MatchMode: mode}
	worker, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); svc.Jobs().Run(worker, svc.Handle, nil) }()
	e = platform.Serve(ctx, "insight-service", p, func(server *grpc.Server) { rpc.RegisterInsightServiceServer(server, &transport.Server{Service: svc}) }, nil)
	cancel()
	wg.Wait()
	if e != nil {
		slog.Error("service failed", "error", e)
		os.Exit(1)
	}
}
