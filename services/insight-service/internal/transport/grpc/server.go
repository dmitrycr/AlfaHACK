package transport

import (
	"alfa.local/insight-service/internal/service"
	"alfa.local/shared/llm"
	"alfa.local/shared/pipeline"
	"alfa.local/shared/platform"
	"alfa.local/shared/rpc"
	"context"
)

type Server struct {
	rpc.UnimplementedInsightServiceServer
	Service service.Service
}

func (s *Server) SubmitMatch(ctx context.Context, r *rpc.Request) (*rpc.Response, error) {
	if e := pipeline.RequireService(ctx, "ml-service", "ranking-service", "scoring-service"); e != nil {
		return nil, e
	}
	if e := platform.RequireCompany(ctx, r.CompanyId); e != nil {
		return nil, e
	}
	var in llm.Match
	if llm.Strict(r.Payload, &in) != nil || in.CompanyID != r.CompanyId {
		return pipeline.Result(nil, llm.ErrInvalid)
	}
	e := s.Service.StoreMatch(ctx, in)
	return pipeline.Result(map[string]bool{"stored": e == nil}, e)
}
func (s *Server) CreateInsight(ctx context.Context, r *rpc.Request) (*rpc.Response, error) {
	if e := platform.RequireCompany(ctx, r.CompanyId); e != nil {
		return nil, e
	}
	var in struct {
		EventID string `json:"event_id"`
		Key     string `json:"idempotency_key"`
	}
	if llm.Strict(r.Payload, &in) != nil {
		return pipeline.Result(nil, llm.ErrInvalid)
	}
	out, e := s.Service.Create(ctx, r.CompanyId, in.EventID, in.Key)
	return pipeline.Result(out, e)
}
func (s *Server) GetInsight(ctx context.Context, r *rpc.Request) (*rpc.Response, error) {
	if e := platform.RequireCompany(ctx, r.CompanyId); e != nil {
		return nil, e
	}
	out, e := s.Service.Jobs().Get(ctx, r.Id, r.CompanyId, "")
	return pipeline.Result(out, e)
}
func (s *Server) ListInsights(ctx context.Context, r *rpc.Request) (*rpc.Response, error) {
	if e := platform.RequireCompany(ctx, r.CompanyId); e != nil {
		return nil, e
	}
	l, o, e := platform.Page(int(r.Limit), int(r.Offset))
	if e != nil {
		return pipeline.Result(nil, llm.ErrInvalid)
	}
	out, e := s.Service.Jobs().List(ctx, "insight", r.CompanyId, l, o)
	return pipeline.Result(map[string]any{"items": out, "limit": l, "offset": o}, e)
}
func (s *Server) GetInsightContext(ctx context.Context, r *rpc.Request) (*rpc.Response, error) {
	if e := pipeline.RequireService(ctx, "ai-agent-service"); e != nil {
		return nil, e
	}
	if e := platform.RequireCompany(ctx, r.CompanyId); e != nil {
		return nil, e
	}
	in, out, e := s.Service.Context(ctx, r.Id, r.CompanyId)
	return pipeline.Result(map[string]any{"context": in, "insight": out}, e)
}
