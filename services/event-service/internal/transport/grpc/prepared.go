package transport

import (
	"alfa.local/shared/pipeline"
	"alfa.local/shared/platform"
	"alfa.local/shared/rpc"
	"context"
)

func (s *Server) GetPreparedEvent(ctx context.Context, r *rpc.Request) (*rpc.Response, error) {
	if e := platform.RequireCompany(ctx, r.CompanyId); e != nil {
		return nil, e
	}
	v, e := s.Store.Prepared(ctx, r.Id, r.CompanyId)
	return pipeline.Result(v, e)
}
func (s *Server) ListPreparedEvents(ctx context.Context, r *rpc.Request) (*rpc.Response, error) {
	if e := platform.RequireCompany(ctx, r.CompanyId); e != nil {
		return nil, e
	}
	l, o, e := platform.Page(int(r.Limit), int(r.Offset))
	if e != nil {
		return pipeline.Result(nil, e)
	}
	v, e := s.Store.ListPrepared(ctx, r.CompanyId, l, o)
	return pipeline.Result(map[string]any{"items": v, "limit": l, "offset": o}, e)
}
