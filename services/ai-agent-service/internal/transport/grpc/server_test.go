package transport

import (
	"alfa.local/shared/llm"
	"alfa.local/shared/llm/llmtest"
	"alfa.local/shared/platform"
	"alfa.local/shared/rpc"
	"context"
	"encoding/json"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"net"
	"strings"
	"testing"
	"time"
)

func TestRunRequiresAuthorizedServiceAndScope(t *testing.T) {
	key := strings.Repeat("k", 32)
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer(grpc.UnaryInterceptor(platform.AuthInterceptor("ai-agent-service", key)))
	rpc.RegisterAIServiceServer(server, &Server{})
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	conn, e := grpc.NewClient("passthrough:///buf", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }))
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	input, _ := json.Marshal(llmtest.Tag())
	payload, _ := json.Marshal(llm.Call{Task: "tag", Scope: "public", Input: input})
	request := &rpc.Request{Payload: payload}
	client := rpc.NewAIServiceClient(conn)
	if _, e = client.Run(context.Background(), request); status.Code(e) != codes.Unauthenticated {
		t.Fatal(e)
	}
	for _, claims := range []platform.Claims{{Subject: "user", CompanyID: "company-test", Audience: "ai-agent-service"}, {Subject: "untrusted-service", Audience: "ai-agent-service", Service: true}, {Subject: "event-service", CompanyID: "company-test", Audience: "ai-agent-service", Service: true}} {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_, e = client.Run(platform.Outgoing(ctx, claims, key), request)
		cancel()
		if status.Code(e) != codes.PermissionDenied {
			t.Fatal(claims.Subject, e)
		}
	}
}
