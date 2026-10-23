package transport

import (
	"alfa.local/ai-agent-service/internal/service"
	"alfa.local/shared/llm"
	"alfa.local/shared/pipeline"
	"alfa.local/shared/platform"
	"alfa.local/shared/rpc"
	"context"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"strings"
)

type Server struct {
	rpc.UnimplementedAIServiceServer
	Service service.Service
}

func (s *Server) Run(ctx context.Context, r *rpc.Request) (*rpc.Response, error) {
	var in llm.Call
	if llm.Strict(r.Payload, &in) != nil {
		return pipeline.Result(nil, llm.ErrInvalid)
	}
	switch in.Task {
	case "tag":
		if e := pipeline.RequireService(ctx, "event-service"); e != nil {
			return nil, e
		}
	case "insight", "check":
		if e := pipeline.RequireService(ctx, "insight-service"); e != nil {
			return nil, e
		}
	default:
		return nil, status.Error(codes.PermissionDenied, "operation_not_allowed")
	}
	c := platform.Principal(ctx)
	if c.CompanyID != r.CompanyId || (in.Scope != "public" && in.Scope != "company:"+r.CompanyId) || (in.Scope == "public" && in.Task != "tag") || (strings.HasPrefix(in.Scope, "company:") && r.CompanyId == "") {
		return nil, status.Error(codes.PermissionDenied, "scope_denied")
	}
	out, e := s.Service.Orchestrator().Run(ctx, in)
	return pipeline.Result(out, e)
}
func (s *Server) GetWorkflow(ctx context.Context, r *rpc.Request) (*rpc.Response, error) {
	if e := platform.RequireCompany(ctx, r.CompanyId); e != nil {
		return nil, e
	}
	out, e := s.Service.Orchestrator().Trace(ctx, r.Id, r.CompanyId)
	return pipeline.Result(out, e)
}
func (s *Server) QueueProfile(ctx context.Context, r *rpc.Request) (*rpc.Response, error) {
	if e := pipeline.RequireService(ctx, "ml-service", "company-service", "insight-service"); e != nil {
		return nil, e
	}
	if e := platform.RequireCompany(ctx, r.CompanyId); e != nil {
		return nil, e
	}
	out, e := s.Service.QueueProfile(ctx, r.CompanyId)
	return pipeline.Result(out, e)
}
func (s *Server) GetProfileJob(ctx context.Context, r *rpc.Request) (*rpc.Response, error) {
	if e := pipeline.RequireService(ctx, "ml-service", "company-service", "insight-service"); e != nil {
		return nil, e
	}
	if e := platform.RequireCompany(ctx, r.CompanyId); e != nil {
		return nil, e
	}
	out, e := s.Service.Jobs().Get(ctx, r.Id, r.CompanyId, "")
	return pipeline.Result(out, e)
}
func (s *Server) GetTrace(ctx context.Context, r *rpc.Request) (*rpc.Response, error) {
	if e := platform.RequireCompany(ctx, r.CompanyId); e != nil {
		return nil, e
	}
	out, e := s.Service.Engine.Trace(ctx, r.Id, r.CompanyId)
	return pipeline.Result(out, e)
}
func (s *Server) CreateThread(ctx context.Context, r *rpc.Request) (*rpc.Response, error) {
	if e := platform.RequireCompany(ctx, r.CompanyId); e != nil {
		return nil, e
	}
	var in struct {
		InsightID string `json:"insight_id"`
		Key       string `json:"idempotency_key"`
	}
	if llm.Strict(r.Payload, &in) != nil {
		return pipeline.Result(nil, llm.ErrInvalid)
	}
	out, e := s.Service.CreateThread(ctx, r.CompanyId, platform.Principal(ctx).Subject, in.InsightID, in.Key)
	return pipeline.Result(out, e)
}
func (s *Server) GetThread(ctx context.Context, r *rpc.Request) (*rpc.Response, error) {
	if e := platform.RequireCompany(ctx, r.CompanyId); e != nil {
		return nil, e
	}
	l, o, e := platform.Page(int(r.Limit), int(r.Offset))
	if e != nil {
		return pipeline.Result(nil, llm.ErrInvalid)
	}
	out, e := s.Service.GetThread(ctx, r.Id, r.CompanyId, platform.Principal(ctx).Subject, l, o)
	return pipeline.Result(out, e)
}
func (s *Server) SendMessage(ctx context.Context, r *rpc.Request) (*rpc.Response, error) {
	if e := platform.RequireCompany(ctx, r.CompanyId); e != nil {
		return nil, e
	}
	var in struct {
		Message string `json:"message"`
		Key     string `json:"idempotency_key"`
	}
	if llm.Strict(r.Payload, &in) != nil {
		return pipeline.Result(nil, llm.ErrInvalid)
	}
	out, e := s.Service.Send(ctx, r.Id, r.CompanyId, platform.Principal(ctx).Subject, in.Message, in.Key)
	return pipeline.Result(out, e)
}
func (s *Server) GetMessage(ctx context.Context, r *rpc.Request) (*rpc.Response, error) {
	if e := platform.RequireCompany(ctx, r.CompanyId); e != nil {
		return nil, e
	}
	out, e := s.Service.Jobs().Get(ctx, r.Id, r.CompanyId, platform.Principal(ctx).Subject)
	return pipeline.Result(out, e)
}
