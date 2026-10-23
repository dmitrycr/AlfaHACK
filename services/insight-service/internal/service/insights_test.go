package service

import (
	"alfa.local/insight-service/migrations"
	"alfa.local/shared/jobs"
	"alfa.local/shared/llm"
	"alfa.local/shared/llm/llmtest"
	"alfa.local/shared/pipeline"
	"alfa.local/shared/platform"
	"alfa.local/shared/rpc"
	"alfa.local/shared/testdb"
	"context"
	"encoding/json"
	"errors"
	"google.golang.org/grpc"
	"testing"
	"time"
)

type companyFake struct {
	rpc.CompanyServiceClient
	in *llm.InsightInput
}

func (f companyFake) GetCompany(context.Context, *rpc.Request, ...grpc.CallOption) (*rpc.Response, error) {
	b, _ := json.Marshal(f.in.Company)
	return &rpc.Response{Payload: b}, nil
}
func (f companyFake) GetProfile(context.Context, *rpc.Request, ...grpc.CallOption) (*rpc.Response, error) {
	b, _ := json.Marshal(f.in.Profile)
	return &rpc.Response{Payload: b}, nil
}

type eventsFake struct {
	rpc.EventServiceClient
	in *llm.InsightInput
}

func (f eventsFake) GetPreparedEvent(context.Context, *rpc.Request, ...grpc.CallOption) (*rpc.Response, error) {
	b, _ := json.Marshal(f.in.Prepared)
	return &rpc.Response{Payload: b}, nil
}

type aiFake struct {
	rpc.AIServiceClient
	engine llm.Engine
}

func (f aiFake) Run(ctx context.Context, r *rpc.Request, _ ...grpc.CallOption) (*rpc.Response, error) {
	var in llm.Call
	if llm.Strict(r.Payload, &in) != nil {
		return nil, llm.ErrInvalid
	}
	v, e := (llm.Orchestrator{Engine: f.engine}).Run(ctx, in)
	return pipeline.Result(v, e)
}
func TestExternalMatchToCheckedCardAndStaleJob(t *testing.T) {
	ctx := context.Background()
	p := testdb.Open(t, "insights", "agent")
	if e := migrations.Apply(ctx, p); e != nil {
		t.Fatal(e)
	}
	if e := platform.MigrateAll(ctx, p, "agent", []platform.Migration{{Version: "001", SQL: llm.Tables}, {Version: "002", SQL: llm.UpgradeTables}}); e != nil {
		t.Fatal(e)
	}
	in := llmtest.Context()
	s := Service{Pool: p, Client: pipeline.Client{Key: "01234567890123456789012345678901", Subject: "insight-service"}, Company: companyFake{in: &in}, Events: eventsFake{in: &in}, AI: aiFake{engine: llm.Engine{Pool: p, Provider: llm.Mock{}, DailyLimitMinor: 50000, MaxParallel: 4, Timeout: time.Second}}, Knowledge: llm.MockKnowledge{}, MatchMode: "external"}
	if _, e := s.Create(ctx, in.Company.ID, in.Prepared.Event.EventID, "card-key-1"); !errors.Is(e, jobs.ErrObsolete) {
		t.Fatal("external mode invented score", e)
	}
	match := in.Match
	match.Synthetic = false
	if e := s.StoreMatch(ctx, match); e != nil {
		t.Fatal(e)
	}
	first, e := s.Create(ctx, in.Company.ID, in.Prepared.Event.EventID, "card-key-1")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Jobs().Once(ctx, s.Handle, nil); e != nil {
		t.Fatal(e)
	}
	ready, e := s.Jobs().Get(ctx, first.ID, in.Company.ID, "")
	var result llm.Insight
	_ = json.Unmarshal(ready.Result, &result)
	if e != nil || ready.Status != "ready" || !result.Check.Passed || len(result.Card.Statements) < 3 {
		t.Fatal(ready, e)
	}
	pending, e := s.Create(ctx, in.Company.ID, in.Prepared.Event.EventID, "card-key-2")
	if e != nil {
		t.Fatal(e)
	}
	in.Profile.Version++
	if _, e = s.Jobs().Once(ctx, s.Handle, nil); e != nil {
		t.Fatal(e)
	}
	stale, e := s.Jobs().Get(ctx, pending.ID, in.Company.ID, "")
	if e != nil || stale.Status != "obsolete" {
		t.Fatal(stale, e)
	}
	retry, e := s.Create(ctx, in.Company.ID, in.Prepared.Event.EventID, "card-key-1")
	if e != nil || retry.ID != first.ID {
		t.Fatal("snapshot update broke idempotency", e)
	}
}
