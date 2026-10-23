package transport

import (
	"context"
	"net"
	"os"
	"testing"
	"time"

	"alfa.local/scoring-service/internal/scoring"
	"alfa.local/shared/platform"
	"alfa.local/shared/rpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

func TestAuthenticatedRanking(t *testing.T) {
	listener := bufconn.Listen(1024 * 1024)
	key := "test-scoring-key-not-a-real-secret"
	server := grpc.NewServer(grpc.UnaryInterceptor(platform.AuthInterceptor("scoring-service", key)))
	rpc.RegisterScoringServiceServer(server, &Server{Config: scoring.DefaultConfig()})
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	conn, err := grpc.NewClient("passthrough:///test", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	client := rpc.NewScoringServiceClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	auth := func(company string, service bool) context.Context {
		return platform.Outgoing(ctx, platform.Claims{Subject: "test", CompanyID: company, Audience: "scoring-service", Service: service}, key)
	}
	data, err := os.ReadFile("../../../../../docs/api/examples/scoring-request.json")
	if err != nil {
		t.Fatal(err)
	}
	request := &rpc.Request{CompanyId: "company-metal", Payload: data}
	for _, tc := range []struct {
		ctx  context.Context
		want codes.Code
	}{
		{ctx, codes.Unauthenticated}, {auth("other", true), codes.PermissionDenied}, {auth("company-metal", false), codes.PermissionDenied}, {auth("company-metal", true), codes.OK},
	} {
		response, err := client.RankEvents(tc.ctx, request)
		if status.Code(err) != tc.want {
			t.Fatalf("want %v: %v", tc.want, err)
		}
		if err == nil {
			var result scoring.Result
			if err = Decode(response.Payload, &result); err != nil || len(result.SelectedEventIDs) != 1 {
				t.Fatalf("result: %+v %v", result, err)
			}
		}
	}
	request.Payload = append(append([]byte{}, data...), []byte(" {}")...)
	if _, err = client.RankEvents(auth("company-metal", true), request); status.Code(err) != codes.InvalidArgument {
		t.Fatal(err)
	}
	request.Payload = []byte(`{"company":{"id":"company-metal"},"unknown":true}`)
	if _, err = client.RankEvents(auth("company-metal", true), request); status.Code(err) != codes.InvalidArgument {
		t.Fatal(err)
	}
}
