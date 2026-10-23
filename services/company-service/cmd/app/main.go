package main

import (
	"alfa.local/company-service/internal/repository/postgres"
	"alfa.local/company-service/internal/service"
	transport "alfa.local/company-service/internal/transport/grpc"
	"alfa.local/company-service/migrations"
	"alfa.local/shared/model"
	"alfa.local/shared/platform"
	"alfa.local/shared/rpc"
	"google.golang.org/grpc"
	"log/slog"
	"os"
)

func main() {
	ctx := platform.Context()
	var companies []model.Company
	var profiles []model.Profile
	importing := len(os.Args) > 1 && os.Args[1] == "import"
	if importing {
		dry, cs, ps, err := readImport()
		if err != nil {
			slog.Error("import invalid", "error", err)
			os.Exit(1)
		}
		if dry {
			return
		}
		companies, profiles = cs, ps
	}
	pool, e := platform.Open(ctx)
	if e != nil {
		slog.Error("database unavailable", "error", e)
		os.Exit(1)
	}
	defer pool.Close()
	if e = migrations.Apply(ctx, pool); e != nil {
		slog.Error("migration failed", "error", e)
		os.Exit(1)
	}
	store := postgres.Store{Pool: pool}
	if importing {
		if e = store.Import(ctx, companies, profiles); e != nil {
			slog.Error("import failed", "error", e)
			os.Exit(1)
		}
		slog.Info("import completed", "companies", len(companies))
		return
	}
	if platform.Env("SEED_DEMO", "false") == "true" {
		if platform.Env("APP_ENV", "development") == "production" {
			slog.Error("demo seed forbidden in production")
			os.Exit(1)
		}
		if e = store.Seed(ctx); e != nil {
			slog.Error("seed failed", "error", e)
			os.Exit(1)
		}
	}
	var registry service.CompanyRegistry
	if platform.Env("APP_ENV", "development") != "production" {
		if platform.Env("COMPANY_REGISTRY_MODE", "mock") != "mock" {
			slog.Error("only mock company registry is implemented")
			os.Exit(1)
		}
		registry = service.NewMockRegistry()
	}
	e = platform.Serve(ctx, "company-service", pool, func(s *grpc.Server) {
		rpc.RegisterCompanyServiceServer(s, &transport.Server{Store: store, Registry: registry})
	}, nil)
	if e != nil {
		slog.Error("service failed", "error", e)
		os.Exit(1)
	}
}
