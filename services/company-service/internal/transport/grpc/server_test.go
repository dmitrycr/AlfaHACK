package transport

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	"alfa.local/company-service/internal/repository/postgres"
	"alfa.local/shared/model"
	"alfa.local/shared/platform"
	"alfa.local/shared/rpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

type memoryStore struct {
	mu      sync.Mutex
	profile model.Profile
}

func (s *memoryStore) Company(context.Context, string) (model.Company, error) {
	return model.Company{ID: "C_EXAMPLE"}, nil
}
func (s *memoryStore) Profile(context.Context, string) (model.Profile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.profile, nil
}
func (s *memoryStore) Replace(_ context.Context, p model.Profile) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p.Version != s.profile.Version+1 {
		return postgres.ErrConflict
	}
	s.profile = p
	return nil
}

func TestFeaturesOverAuthenticatedGRPC(t *testing.T) {
	listener := bufconn.Listen(1024 * 1024)
	key := "local-test-key-not-a-secret"
	store := &memoryStore{}
	srv := grpc.NewServer(grpc.UnaryInterceptor(platform.AuthInterceptor("company-service", key)))
	rpc.RegisterCompanyServiceServer(srv, &Server{Store: store})
	go func() { _ = srv.Serve(listener) }()
	t.Cleanup(srv.Stop)
	conn, err := grpc.NewClient("passthrough:///test", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	client := rpc.NewCompanyServiceClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	auth := func(id string, service bool) context.Context {
		return platform.Outgoing(ctx, platform.Claims{Subject: "test", CompanyID: id, Audience: "company-service", Service: service}, key)
	}
	b, err := os.ReadFile("../../../../../docs/api/examples/profile-features.json")
	if err != nil {
		t.Fatal(err)
	}
	request := &rpc.Request{CompanyId: "C_EXAMPLE", Payload: b}
	if _, err = client.ReplaceProfile(ctx, request); status.Code(err) != codes.Unauthenticated {
		t.Fatal("missing auth", err)
	}
	if _, err = client.ReplaceProfile(auth("other", true), request); status.Code(err) != codes.PermissionDenied {
		t.Fatal("cross company", err)
	}
	if _, err = client.ReplaceProfile(auth("C_EXAMPLE", false), request); status.Code(err) != codes.PermissionDenied {
		t.Fatal("user write", err)
	}
	if _, err = client.ReplaceProfile(auth("C_EXAMPLE", true), request); err != nil {
		t.Fatal(err)
	}
	got, err := client.GetProfile(auth("C_EXAMPLE", false), &rpc.Request{CompanyId: "C_EXAMPLE"})
	if err != nil {
		t.Fatal(err)
	}
	var want, actual model.Profile
	if err = json.Unmarshal(b, &want); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(got.Payload, &actual); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(want, actual) {
		t.Fatal("profile changed during gRPC round trip")
	}
	if _, err = client.ReplaceProfile(auth("C_EXAMPLE", true), request); status.Code(err) != codes.Aborted {
		t.Fatal("version conflict", err)
	}
	var doc map[string]any
	_ = json.Unmarshal(b, &doc)
	doc["version"] = 2
	doc["features"].(map[string]any)["raw_payments"] = []any{}
	bad, _ := json.Marshal(doc)
	for _, payload := range [][]byte{bad, append(append([]byte{}, b...), []byte(" {}")...)} {
		if _, err = client.ReplaceProfile(auth("C_EXAMPLE", true), &rpc.Request{CompanyId: "C_EXAMPLE", Payload: payload}); status.Code(err) != codes.InvalidArgument {
			t.Fatal("unknown data silently accepted", err)
		}
	}
	delete(doc["features"].(map[string]any), "raw_payments")
	doc["features"] = map[string]any{"buyers": nil, "suppliers": nil}
	updated, _ := json.Marshal(doc)
	if _, err = client.ReplaceProfile(auth("C_EXAMPLE", true), &rpc.Request{CompanyId: "C_EXAMPLE", Payload: updated}); err != nil {
		t.Fatal(err)
	}
	got, err = client.GetProfile(auth("C_EXAMPLE", false), &rpc.Request{CompanyId: "C_EXAMPLE"})
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(got.Payload, &actual); err != nil {
		t.Fatal(err)
	}
	if actual.Version != 2 || actual.Features == nil || actual.Features.Buyers != nil {
		t.Fatal("updated cached document is stale or null became zero")
	}
}
