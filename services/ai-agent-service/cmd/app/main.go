package main

import (
	"alfa.local/ai-agent-service/internal/service"
	transport "alfa.local/ai-agent-service/internal/transport/grpc"
	"alfa.local/ai-agent-service/migrations"
	"alfa.local/shared/llm"
	"alfa.local/shared/pipeline"
	"alfa.local/shared/platform"
	"alfa.local/shared/rpc"
	"context"
	"encoding/json"
	"google.golang.org/grpc"
	"log/slog"
	"os"
	"sync"
)

func main() {
	ctx := platform.Context()
	cfg, e := llm.LoadConfig(os.Getenv)
	if e != nil {
		slog.Error("invalid LLM configuration")
		os.Exit(1)
	}
	if len(os.Args) == 2 && os.Args[1] == "check-llm-config" {
		_ = json.NewEncoder(os.Stdout).Encode(cfg.Preflight())
		return
	}
	provider, closeProvider, e := cfg.NewProvider()
	if e != nil {
		slog.Error("invalid LLM provider configuration")
		os.Exit(1)
	}
	defer closeProvider()
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
	insights, e := platform.Dial(platform.Env("INSIGHTS_ADDR", "insight-service:9090"))
	if e != nil {
		panic(e)
	}
	defer insights.Close()
	engine := llm.Engine{Pool: p, Provider: provider, DailyLimitMinor: cfg.DailyLimitMinor, MaxParallel: cfg.MaxParallel, Timeout: cfg.Timeout, Routing: cfg.Routing}
	company, e := platform.Dial(platform.Env("COMPANY_ADDR", "company-service:9090"))
	if e != nil {
		panic(e)
	}
	defer company.Close()
	svc := service.Service{Pool: p, Engine: engine, Client: pipeline.Client{Key: platform.Required("INTERNAL_KEY"), Subject: "ai-agent-service"}, Insights: rpc.NewInsightServiceClient(insights), Company: rpc.NewCompanyServiceClient(company), AllowKimi: cfg.Routing.AllowKimi}
	if len(os.Args) > 1 {
		if os.Args[1] != "prepare-profiles" || len(os.Args) < 3 || len(os.Args) > 502 {
			slog.Error("usage: app prepare-profiles <company-id> ... (max 500)")
			os.Exit(1)
		}
		for _, companyID := range os.Args[2:] {
			job, err := svc.QueueProfile(ctx, companyID)
			if err != nil {
				slog.Error("profile queue failed")
				os.Exit(1)
			}
			_ = json.NewEncoder(os.Stdout).Encode(map[string]string{"job_id": job.ID, "company_id": companyID, "status": job.Status})
		}
		return
	}
	worker, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); svc.Jobs().Run(worker, svc.Handle, nil) }()
	e = platform.Serve(ctx, "ai-agent-service", p, func(server *grpc.Server) { rpc.RegisterAIServiceServer(server, &transport.Server{Service: svc}) }, nil)
	cancel()
	wg.Wait()
	if e != nil {
		slog.Error("service failed", "error", e)
		os.Exit(1)
	}
}
