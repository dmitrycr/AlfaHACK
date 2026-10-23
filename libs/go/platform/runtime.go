package platform

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func Env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
func Required(key string) string {
	v := os.Getenv(key)
	if v == "" {
		slog.Error("missing configuration", "key", key)
		os.Exit(1)
	}
	return v
}
func IntEnv(key string, defaultValue int) int {
	v := os.Getenv(key)
	if v == "" {
		return defaultValue
	}
	n, e := strconv.Atoi(v)
	if e != nil || n <= 0 {
		slog.Error("invalid integer configuration", "key", key)
		os.Exit(1)
	}
	return n
}
func Context() context.Context {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	ctx, _ := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	return ctx
}
func Open(ctx context.Context) (*pgxpool.Pool, error) {
	cfg, e := pgxpool.ParseConfig(Required("DATABASE_URL"))
	if e != nil {
		return nil, e
	}
	cfg.MaxConns = 5
	cfg.ConnConfig.RuntimeParams["statement_timeout"] = "10000"
	p, e := pgxpool.NewWithConfig(ctx, cfg)
	if e != nil {
		return nil, e
	}
	c, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if e = p.Ping(c); e != nil {
		p.Close()
		return nil, e
	}
	return p, nil
}
func Migrate(ctx context.Context, p *pgxpool.Pool, schema, sql string) error {
	return MigrateAll(ctx, p, schema, []Migration{{Version: "001", SQL: sql}})
}

type Migration struct {
	Version string
	SQL     string
}

func MigrateAll(ctx context.Context, p *pgxpool.Pool, schema string, migrations []Migration) error {
	switch schema {
	case "auth", "company", "news", "events", "analytics", "agent", "insights":
	default:
		return errors.New("invalid migration schema")
	}
	tx, e := p.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext($1))", schema); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, "CREATE TABLE IF NOT EXISTS "+schema+".schema_migrations(version text PRIMARY KEY, checksum text NOT NULL, applied_at timestamptz NOT NULL DEFAULT now())"); e != nil {
		return e
	}
	previous := ""
	for _, migration := range migrations {
		if migration.Version <= previous || migration.Version == "" || strings.TrimSpace(migration.SQL) == "" {
			return errors.New("migrations must have ordered unique versions and nonempty SQL")
		}
		previous = migration.Version
		var checksum string
		e = tx.QueryRow(ctx, "SELECT checksum FROM "+schema+".schema_migrations WHERE version=$1", migration.Version).Scan(&checksum)
		if e == nil {
			if checksum != Hash(migration.SQL) {
				return errors.New("applied migration checksum changed")
			}
			continue
		}
		if !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		if _, e = tx.Exec(ctx, migration.SQL); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, "INSERT INTO "+schema+".schema_migrations(version,checksum) VALUES($1,$2)", migration.Version, Hash(migration.SQL)); e != nil {
			return e
		}
	}
	return tx.Commit(ctx)
}
func Dial(address string) (*grpc.ClientConn, error) {
	if address == "" {
		return nil, errors.New("missing RPC address")
	}
	return grpc.NewClient(address, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(3<<20)))
}
func Serve(ctx context.Context, name string, p *pgxpool.Pool, register func(*grpc.Server), mux *http.ServeMux) error {
	if mux == nil {
		mux = http.NewServeMux()
	}
	if _, pattern := mux.Handler(&http.Request{Method: "GET", URL: mustURL("/metrics")}); pattern == "" {
		mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
			s := p.Stat()
			w.Header().Set("Content-Type", "text/plain; version=0.0.4")
			fmt.Fprintf(w, "alfa_db_connections{service=%q} %d\nalfa_db_acquired_connections{service=%q} %d\n", name, s.TotalConns(), name, s.AcquiredConns())
		})
	}
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		JSON(w, 200, map[string]string{"status": "ok", "service": name})
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		c, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		if e := p.Ping(c); e != nil {
			JSON(w, 503, map[string]string{"status": "not_ready"})
			return
		}
		JSON(w, 200, map[string]string{"status": "ready"})
	})
	httpServer := &http.Server{Addr: Env("HTTP_ADDR", ":8080"), Handler: Recover(mux), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	key := Required("INTERNAL_KEY")
	if len(key) < 32 {
		return errors.New("INTERNAL_KEY must contain at least 32 bytes")
	}
	var rpcServer *grpc.Server
	errs := make(chan error, 2)
	if register != nil {
		listener, e := net.Listen("tcp", Env("GRPC_ADDR", ":9090"))
		if e != nil {
			return e
		}
		rpcServer = grpc.NewServer(grpc.UnaryInterceptor(AuthInterceptor(name, key)), grpc.MaxRecvMsgSize(3<<20))
		register(rpcServer)
		go func() { errs <- rpcServer.Serve(listener) }()
	}
	go func() { slog.Info("service started", "service", name); errs <- httpServer.ListenAndServe() }()
	select {
	case <-ctx.Done():
	case e := <-errs:
		if e != nil && !errors.Is(e, http.ErrServerClosed) {
			return e
		}
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(shutdown)
	if rpcServer != nil {
		done := make(chan struct{})
		go func() { rpcServer.GracefulStop(); close(done) }()
		select {
		case <-done:
		case <-shutdown.Done():
			rpcServer.Stop()
		}
	}
	return nil
}
func JSON(w http.ResponseWriter, code int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(value)
}
func Decode(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if e := d.Decode(dst); e != nil {
		return e
	}
	var tail any
	if e := d.Decode(&tail); e == nil {
		return errors.New("multiple JSON values")
	} else if !errors.Is(e, io.EOF) {
		return e
	}
	return nil
}
func Recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := ID()
		w.Header().Set("X-Request-ID", id)
		defer func() {
			if recover() != nil {
				slog.Error("request panic", "request_id", id)
				JSON(w, 500, map[string]string{"code": "internal_error", "request_id": id})
			}
		}()
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey{}, id)))
	})
}

type requestIDKey struct{}

func RequestID(ctx context.Context) string {
	v, _ := ctx.Value(requestIDKey{}).(string)
	if v == "" {
		return ID()
	}
	return v
}
func mustURL(path string) *url.URL { return &url.URL{Path: path} }
func Page(limit, offset int) (int, int, error) {
	if limit == 0 {
		limit = 20
	}
	if limit < 1 || limit > 100 || offset < 0 {
		return 0, 0, errors.New("invalid pagination")
	}
	return limit, offset, nil
}
func ScopeCompany(scope string) string { return strings.TrimPrefix(scope, "company:") }
