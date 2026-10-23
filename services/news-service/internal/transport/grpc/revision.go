package transport

import (
	"alfa.local/shared/llm"
	"alfa.local/shared/pipeline"
	"alfa.local/shared/platform"
	"alfa.local/shared/rpc"
	"context"
)

func (s *Server) GetArticleRevision(ctx context.Context, r *rpc.Request) (*rpc.Response, error) {
	if e := pipeline.RequireService(ctx, "event-service"); e != nil {
		return nil, e
	}
	if platform.Principal(ctx).CompanyID != r.CompanyId {
		return pipeline.Result(nil, llm.ErrInvalid)
	}
	var in struct {
		Revision int `json:"revision"`
	}
	if llm.Strict(r.Payload, &in) != nil || in.Revision < 1 {
		return pipeline.Result(nil, llm.ErrInvalid)
	}
	out, e := s.Store.ArticleRevision(ctx, r.Id, r.CompanyId, in.Revision)
	return pipeline.Result(out, e)
}
