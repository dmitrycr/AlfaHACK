package transport

import (
	"alfa.local/analytics-service/internal/service"
	"alfa.local/shared/platform"
	"alfa.local/shared/rpc"
	"context"
	"encoding/json"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type Server struct {
	rpc.UnimplementedAnalyticsServiceServer
	Store service.Store
}

func (s *Server) GetSummary(ctx context.Context, r *rpc.Request) (*rpc.Response, error) {
	if e := platform.RequireCompany(ctx, r.CompanyId); e != nil {
		return nil, e
	}
	v, e := s.Store.Summary(ctx, r.CompanyId)
	if e != nil {
		return nil, status.Error(codes.Unavailable, "analytics unavailable")
	}
	b, _ := json.Marshal(map[string]any{"counters": v, "source_mode": "fixture", "llm_calls": 0, "ml_quality_evaluated": false})
	return &rpc.Response{Payload: b}, nil
}
