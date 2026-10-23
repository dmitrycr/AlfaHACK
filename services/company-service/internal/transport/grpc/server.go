package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"

	"alfa.local/company-service/internal/repository/postgres"
	"alfa.local/company-service/internal/service"
	"alfa.local/shared/model"
	"alfa.local/shared/platform"
	"alfa.local/shared/rpc"
	"github.com/jackc/pgx/v5"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type Server struct {
	rpc.UnimplementedCompanyServiceServer
	Store    ProfileStore
	Registry service.CompanyRegistry
}

type ProfileStore interface {
	Company(context.Context, string) (model.Company, error)
	Profile(context.Context, string) (model.Profile, error)
	Replace(context.Context, model.Profile) error
}

func (s *Server) checkCompany(ctx context.Context, r *rpc.Request, ensure bool) (*rpc.Response, error) {
	c := platform.Principal(ctx)
	if !c.Service || c.Subject != "api-gateway-registration" {
		return nil, status.Error(codes.PermissionDenied, "registration service credentials required")
	}
	var input model.CompanyCheckInput
	if e := json.Unmarshal(r.Payload, &input); e != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid_input")
	}
	check, e := service.CheckCompany(ctx, s.Registry, input, time.Now())
	if e != nil {
		switch {
		case errors.Is(e, service.ErrInvalidINN), errors.Is(e, service.ErrInvalidName):
			return nil, status.Error(codes.InvalidArgument, e.Error())
		case errors.Is(e, service.ErrCompanyNotFound), errors.Is(e, service.ErrNameMismatch):
			return nil, status.Error(codes.FailedPrecondition, e.Error())
		default:
			return nil, status.Error(codes.Unavailable, "registry_unavailable")
		}
	}
	if ensure {
		ensurer, ok := s.Store.(interface {
			EnsureCompany(context.Context, model.CompanyCheck, string) (model.CompanyCheck, error)
		})
		if !ok {
			return nil, status.Error(codes.Unavailable, "registry_storage_unavailable")
		}
		check, e = ensurer.EnsureCompany(ctx, check, input.Name)
	}
	return response(check, e)
}
func (s *Server) CheckCompany(ctx context.Context, r *rpc.Request) (*rpc.Response, error) {
	return s.checkCompany(ctx, r, false)
}
func (s *Server) EnsureCompany(ctx context.Context, r *rpc.Request) (*rpc.Response, error) {
	return s.checkCompany(ctx, r, true)
}

func response(v any, e error) (*rpc.Response, error) {
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "company or profile not found")
	}
	if e != nil {
		return nil, status.Error(codes.Internal, "storage unavailable")
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, status.Error(codes.Internal, "invalid stored document")
	}
	return &rpc.Response{Payload: b}, nil
}
func (s *Server) GetCompany(ctx context.Context, r *rpc.Request) (*rpc.Response, error) {
	if e := platform.RequireCompany(ctx, r.CompanyId); e != nil {
		return nil, e
	}
	v, e := s.Store.Company(ctx, r.CompanyId)
	return response(v, e)
}
func (s *Server) GetProfile(ctx context.Context, r *rpc.Request) (*rpc.Response, error) {
	if e := platform.RequireCompany(ctx, r.CompanyId); e != nil {
		return nil, e
	}
	v, e := s.Store.Profile(ctx, r.CompanyId)
	return response(v, e)
}
func (s *Server) ReplaceProfile(ctx context.Context, r *rpc.Request) (*rpc.Response, error) {
	if e := platform.RequireCompany(ctx, r.CompanyId); e != nil {
		return nil, e
	}
	if !platform.Principal(ctx).Service {
		return nil, status.Error(codes.PermissionDenied, "service credentials required")
	}
	var p model.Profile
	decoder := json.NewDecoder(bytes.NewReader(r.Payload))
	decoder.DisallowUnknownFields()
	if e := decoder.Decode(&p); e != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid JSON")
	}
	if e := decoder.Decode(new(any)); e != io.EOF {
		return nil, status.Error(codes.InvalidArgument, "unexpected JSON tail")
	}
	if p.CompanyID != r.CompanyId {
		return nil, status.Error(codes.PermissionDenied, "company mismatch")
	}
	if e := service.ValidateProfile(p); e != nil {
		return nil, status.Error(codes.InvalidArgument, e.Error())
	}
	e := s.Store.Replace(ctx, p)
	if errors.Is(e, postgres.ErrConflict) {
		return nil, status.Error(codes.Aborted, "profile version conflict")
	}
	return response(p, e)
}
