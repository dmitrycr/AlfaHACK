package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"alfa.local/scoring-service/internal/scoring"
	transport "alfa.local/scoring-service/internal/transport/grpc"
	"alfa.local/shared/platform"
	"alfa.local/shared/rpc"
	"google.golang.org/grpc"
)

func read(path string, out any) error {
	stream, err := os.Open(path)
	if err != nil {
		return err
	}
	defer stream.Close()
	data, err := io.ReadAll(io.LimitReader(stream, (3<<20)+1))
	if err != nil {
		return err
	}
	if len(data) > 3<<20 {
		return errors.New("input exceeds 3 MiB")
	}
	return transport.Decode(data, out)
}
func run() error {
	args := os.Args[1:]
	mode := "serve"
	if len(args) > 0 && (args[0] == "rank" || args[0] == "serve") {
		mode = args[0]
		args = args[1:]
	}
	flags := flag.NewFlagSet(mode, flag.ContinueOnError)
	input := flags.String("input", "", "ranking request JSON (rank mode)")
	output := flags.String("output", "", "result JSON; empty uses stdout")
	config := flags.String("config", os.Getenv("SCORING_CONFIG"), "versioned JSON rules")
	if err := flags.Parse(args); err != nil {
		return err
	}
	cfg := scoring.DefaultConfig()
	if *config != "" {
		if err := read(*config, &cfg); err != nil {
			return err
		}
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	if mode == "rank" {
		var request scoring.Request
		if err := read(*input, &request); err != nil {
			return err
		}
		result, err := cfg.Rank(request)
		if err != nil {
			return err
		}
		data, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			return err
		}
		data = append(data, '\n')
		if *output != "" {
			return os.WriteFile(*output, data, 0600)
		}
		_, err = os.Stdout.Write(data)
		return err
	}
	key := platform.Required("INTERNAL_KEY")
	if len(key) < 32 {
		return errors.New("INTERNAL_KEY must contain at least 32 bytes")
	}
	ctx := platform.Context()
	grpcListener, err := net.Listen("tcp", platform.Env("GRPC_ADDR", ":9090"))
	if err != nil {
		return err
	}
	defer grpcListener.Close()
	httpListener, err := net.Listen("tcp", platform.Env("HTTP_ADDR", ":8080"))
	if err != nil {
		return err
	}
	defer httpListener.Close()
	server := grpc.NewServer(grpc.UnaryInterceptor(platform.AuthInterceptor("scoring-service", key)), grpc.MaxRecvMsgSize(3<<20))
	rpc.RegisterScoringServiceServer(server, &transport.Server{Config: cfg})
	mux := http.NewServeMux()
	for _, path := range []string{"/healthz", "/readyz"} {
		mux.HandleFunc("GET "+path, func(w http.ResponseWriter, r *http.Request) {
			platform.JSON(w, 200, map[string]any{"status": "ready", "service": "scoring-service", "ruleset_version": cfg.Version})
		})
	}
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		fmt.Fprintln(w, "alfa_scoring_llm_calls_total 0")
	})
	httpServer := &http.Server{Handler: platform.Recover(mux), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second}
	errs := make(chan error, 2)
	go func() { errs <- server.Serve(grpcListener) }()
	go func() { errs <- httpServer.Serve(httpListener) }()
	slog.Info("scoring service started", "ruleset_version", cfg.Version)
	var serveErr error
	select {
	case <-ctx.Done():
	case serveErr = <-errs:
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(shutdown)
	done := make(chan struct{})
	go func() { server.GracefulStop(); close(done) }()
	select {
	case <-done:
	case <-shutdown.Done():
		server.Stop()
	}
	if errors.Is(serveErr, http.ErrServerClosed) || errors.Is(serveErr, grpc.ErrServerStopped) {
		return nil
	}
	return serveErr
}
func main() {
	if err := run(); err != nil {
		slog.Error("scoring failed", "error", err)
		os.Exit(1)
	}
}
