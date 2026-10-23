package transport

import (
	"alfa.local/api-gateway/internal/service"
	"alfa.local/shared/pipeline"
	"alfa.local/shared/platform"
	"alfa.local/shared/rpc"
	"context"
	"encoding/json"
	"net/http"
	"strings"
)

func (a *API) pipelineRoutes(m *http.ServeMux) {
	m.HandleFunc("GET /api/v1/events/prepared", a.protect(a.proxy("event-service", func(ctx context.Context, r *rpc.Request) (*rpc.Response, error) {
		return a.Events.ListPreparedEvents(ctx, r)
	})))
	m.HandleFunc("GET /api/v1/events/{id}/prepared", a.protect(a.proxy("event-service", func(ctx context.Context, r *rpc.Request) (*rpc.Response, error) {
		return a.Events.GetPreparedEvent(ctx, r)
	})))
	m.HandleFunc("POST /api/v1/insights", a.protect(a.createInsight))
	m.HandleFunc("GET /api/v1/insights", a.protect(a.proxy("insight-service", func(ctx context.Context, r *rpc.Request) (*rpc.Response, error) {
		return a.Insights.ListInsights(ctx, r)
	})))
	m.HandleFunc("GET /api/v1/insights/{id}", a.protect(a.proxy("insight-service", func(ctx context.Context, r *rpc.Request) (*rpc.Response, error) { return a.Insights.GetInsight(ctx, r) })))
	m.HandleFunc("POST /api/v1/chat/threads", a.protect(a.createThread))
	m.HandleFunc("GET /api/v1/chat/threads/{id}", a.protect(a.proxy("ai-agent-service", func(ctx context.Context, r *rpc.Request) (*rpc.Response, error) { return a.AI.GetThread(ctx, r) })))
	m.HandleFunc("POST /api/v1/chat/threads/{id}/messages", a.protect(a.sendMessage))
	m.HandleFunc("GET /api/v1/chat/messages/{id}", a.protect(a.proxy("ai-agent-service", func(ctx context.Context, r *rpc.Request) (*rpc.Response, error) { return a.AI.GetMessage(ctx, r) })))
	m.HandleFunc("GET /api/v1/ai/runs/{id}", a.protect(a.proxy("ai-agent-service", func(ctx context.Context, r *rpc.Request) (*rpc.Response, error) { return a.AI.GetTrace(ctx, r) })))
	m.HandleFunc("GET /api/v1/ai/workflows/{id}", a.protect(a.proxy("ai-agent-service", func(ctx context.Context, r *rpc.Request) (*rpc.Response, error) { return a.AI.GetWorkflow(ctx, r) })))
}
func (a *API) pipelinePost(w http.ResponseWriter, r *http.Request, s service.Session, target string, input map[string]string, fn call) {
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if !pipeline.ValidKey(key) {
		fail(w, 400, "invalid_idempotency_key")
		return
	}
	input["idempotency_key"] = key
	b, _ := json.Marshal(input)
	ctx, cancel := a.rpcContext(r, s, target)
	defer cancel()
	out, e := fn(ctx, &rpc.Request{CompanyId: s.User.CompanyID, Id: r.PathValue("id"), Payload: b})
	writeRPC(w, 202, out, e)
}
func (a *API) createInsight(w http.ResponseWriter, r *http.Request, s service.Session) {
	var in struct {
		EventID string `json:"event_id"`
	}
	if platform.Decode(w, r, &in) != nil {
		fail(w, 400, "invalid_input")
		return
	}
	a.pipelinePost(w, r, s, "insight-service", map[string]string{"event_id": in.EventID}, func(ctx context.Context, r *rpc.Request) (*rpc.Response, error) {
		return a.Insights.CreateInsight(ctx, r)
	})
}
func (a *API) createThread(w http.ResponseWriter, r *http.Request, s service.Session) {
	var in struct {
		InsightID string `json:"insight_id"`
	}
	if platform.Decode(w, r, &in) != nil {
		fail(w, 400, "invalid_input")
		return
	}
	a.pipelinePost(w, r, s, "ai-agent-service", map[string]string{"insight_id": in.InsightID}, func(ctx context.Context, r *rpc.Request) (*rpc.Response, error) { return a.AI.CreateThread(ctx, r) })
}
func (a *API) sendMessage(w http.ResponseWriter, r *http.Request, s service.Session) {
	var in struct {
		Message string `json:"message"`
	}
	if platform.Decode(w, r, &in) != nil {
		fail(w, 400, "invalid_input")
		return
	}
	a.pipelinePost(w, r, s, "ai-agent-service", map[string]string{"message": in.Message}, func(ctx context.Context, r *rpc.Request) (*rpc.Response, error) { return a.AI.SendMessage(ctx, r) })
}
