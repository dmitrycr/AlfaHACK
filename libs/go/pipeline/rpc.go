// Package pipeline contains shared adapters, not business ownership or storage.
package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"time"

	"alfa.local/shared/jobs"
	"alfa.local/shared/llm"
	"alfa.local/shared/platform"
	"alfa.local/shared/rpc"
	"github.com/jackc/pgx/v5"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"strconv"
)

var keyPattern = regexp.MustCompile(`^[A-Za-z0-9_.:-]{8,128}$`)

func ValidKey(s string) bool { return keyPattern.MatchString(s) }
func Result(v any, e error) (*rpc.Response, error) {
	if e != nil {
		if errors.Is(e, llm.ErrBudget) {
			e = &llm.ProviderError{Code: "llm_budget_exceeded"}
		}
		var providerError *llm.ProviderError
		if errors.As(e, &providerError) {
			code := codes.Unavailable
			if providerError.Terminal() {
				code = codes.FailedPrecondition
			}
			if providerError.Code == "llm_budget_exceeded" {
				code = codes.ResourceExhausted
			}
			out := status.New(code, providerError.Code)
			with, err := out.WithDetails(&errdetails.ErrorInfo{Reason: providerError.Code, Domain: "alfa.llm", Metadata: map[string]string{"terminal": strconv.FormatBool(providerError.Terminal()), "uncertain": strconv.FormatBool(providerError.Uncertain()), "delay": providerError.RetryDelay().String()}})
			if err != nil {
				return nil, out.Err()
			}
			return nil, with.Err()
		}
		switch {
		case errors.Is(e, pgx.ErrNoRows):
			return nil, status.Error(codes.NotFound, "not_found")
		case errors.Is(e, llm.ErrInvalid):
			return nil, status.Error(codes.InvalidArgument, "invalid_input")
		case errors.Is(e, jobs.ErrConflict):
			return nil, status.Error(codes.AlreadyExists, "idempotency_conflict")
		case errors.Is(e, jobs.ErrObsolete):
			return nil, status.Error(codes.FailedPrecondition, "snapshot_not_current")
		case errors.Is(e, llm.ErrBudget):
			return nil, status.Error(codes.ResourceExhausted, "llm_budget_exceeded")
		case errors.Is(e, llm.ErrBusy):
			return nil, status.Error(codes.Unavailable, "processing_in_progress")
		default:
			if status.Code(e) != codes.Unknown {
				return nil, e
			}
			return nil, status.Error(codes.Unavailable, "processing_unavailable")
		}
	}
	b, e := json.Marshal(v)
	if e != nil {
		return nil, status.Error(codes.Internal, "invalid_result")
	}
	return &rpc.Response{Payload: b}, nil
}

type Method func(context.Context, *rpc.Request, ...grpc.CallOption) (*rpc.Response, error)
type Client struct{ Key, Subject string }

func (c Client) Call(ctx context.Context, target, company, id string, input any, method Method, out any) error {
	b, e := json.Marshal(input)
	if e != nil {
		return e
	}
	call, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	call = platform.Outgoing(call, platform.Claims{Subject: c.Subject, CompanyID: company, Audience: target, Service: true, CorrelationID: platform.RequestID(ctx)}, c.Key)
	r, e := method(call, &rpc.Request{CompanyId: company, Id: id, Payload: b})
	if e != nil {
		return e
	}
	return llm.Strict(r.Payload, out)
}
func (c Client) Generate(ctx context.Context, company, scope, task string, input any, client rpc.AIServiceClient) (llm.Completion, error) {
	b, e := json.Marshal(input)
	if e != nil {
		return llm.Completion{}, e
	}
	var out llm.Completion
	call, cancel := context.WithTimeout(ctx, 110*time.Second)
	defer cancel()
	call = platform.Outgoing(call, platform.Claims{Subject: c.Subject, CompanyID: company, Audience: "ai-agent-service", Service: true, CorrelationID: platform.RequestID(ctx)}, c.Key)
	payload, _ := json.Marshal(llm.Call{Task: task, Scope: scope, Input: b})
	r, e := client.Run(call, &rpc.Request{CompanyId: company, Payload: payload})
	if e == nil {
		e = llm.Strict(r.Payload, &out)
	}
	return out, e
}
func RequireService(ctx context.Context, names ...string) error {
	c := platform.Principal(ctx)
	if !c.Service || !llm.Contains(names, c.Subject) {
		return status.Error(codes.PermissionDenied, "service_credentials_required")
	}
	return nil
}
