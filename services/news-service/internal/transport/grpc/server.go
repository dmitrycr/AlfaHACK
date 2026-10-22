package transport

import (
	"context"
	"encoding/json"
	"errors"

	"alfa.local/news-service/internal/repository/postgres"
	"alfa.local/news-service/internal/service"
	"alfa.local/news-service/internal/source"
	"alfa.local/shared/platform"
	"alfa.local/shared/rpc"
	"github.com/jackc/pgx/v5"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type Server struct {
	rpc.UnimplementedNewsServiceServer
	Store  postgres.Store
	Config source.Config
}

func result(v any, e error) (*rpc.Response, error) {
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "record not found")
	}
	if errors.Is(e, postgres.ErrConflict) {
		return nil, status.Error(codes.AlreadyExists, "idempotency key reused with different input")
	}
	if e != nil {
		return nil, status.Error(codes.Unavailable, "storage unavailable")
	}
	b, _ := json.Marshal(v)
	return &rpc.Response{Payload: b}, nil
}
func (s *Server) ListArticles(ctx context.Context, r *rpc.Request) (*rpc.Response, error) {
	if e := platform.RequireCompany(ctx, r.CompanyId); e != nil {
		return nil, e
	}
	limit, offset, e := platform.Page(int(r.Limit), int(r.Offset))
	if e != nil {
		return nil, status.Error(codes.InvalidArgument, e.Error())
	}
	v, e := s.Store.Articles(ctx, r.CompanyId, limit, offset)
	return result(v, e)
}
func (s *Server) GetArticle(ctx context.Context, r *rpc.Request) (*rpc.Response, error) {
	if e := platform.RequireCompany(ctx, r.CompanyId); e != nil {
		return nil, e
	}
	v, e := s.Store.Article(ctx, r.Id, r.CompanyId)
	return result(v, e)
}
func (s *Server) Submit(ctx context.Context, r *rpc.Request) (*rpc.Response, error) {
	if e := platform.RequireCompany(ctx, r.CompanyId); e != nil {
		return nil, e
	}
	var in service.SubmitInput
	if json.Unmarshal(r.Payload, &in) != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid input")
	}
	j, e := service.PrepareSubmission(in, s.Config)
	if e != nil {
		return nil, status.Error(codes.InvalidArgument, e.Error())
	}
	j.CorrelationID = platform.Principal(ctx).CorrelationID
	v, e := s.Store.Submit(ctx, r.CompanyId, platform.Principal(ctx).Subject, in.Key, service.RequestHash(in), j)
	return result(v, e)
}
func (s *Server) GetSubmission(ctx context.Context, r *rpc.Request) (*rpc.Response, error) {
	if e := platform.RequireCompany(ctx, r.CompanyId); e != nil {
		return nil, e
	}
	v, e := s.Store.Submission(ctx, r.Id, r.CompanyId)
	return result(v, e)
}
