package transport

import (
	"alfa.local/event-service/internal/service"
	"alfa.local/shared/platform"
	"alfa.local/shared/rpc"
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type Server struct {
	rpc.UnimplementedEventServiceServer
	Store service.Store
}

func result(v any, e error) (*rpc.Response, error) {
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "event not found")
	}
	if e != nil {
		return nil, status.Error(codes.Unavailable, "storage unavailable")
	}
	b, _ := json.Marshal(v)
	return &rpc.Response{Payload: b}, nil
}
func (s *Server) GetEvent(ctx context.Context, r *rpc.Request) (*rpc.Response, error) {
	if e := platform.RequireCompany(ctx, r.CompanyId); e != nil {
		return nil, e
	}
	v, e := s.Store.Get(ctx, r.Id, r.CompanyId)
	return result(v, e)
}
func (s *Server) ListEvents(ctx context.Context, r *rpc.Request) (*rpc.Response, error) {
	if e := platform.RequireCompany(ctx, r.CompanyId); e != nil {
		return nil, e
	}
	l, o, e := platform.Page(int(r.Limit), int(r.Offset))
	if e != nil {
		return nil, status.Error(codes.InvalidArgument, e.Error())
	}
	v, e := s.Store.List(ctx, r.CompanyId, l, o)
	return result(v, e)
}
