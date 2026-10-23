package service

import (
	"alfa.local/ai-agent-service/migrations"
	"alfa.local/shared/llm"
	"alfa.local/shared/llm/llmtest"
	"alfa.local/shared/pipeline"
	"alfa.local/shared/rpc"
	"alfa.local/shared/testdb"
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"google.golang.org/grpc"
	"testing"
	"time"
)

type insightClient struct{ rpc.InsightServiceClient }

func (insightClient) GetInsightContext(context.Context, *rpc.Request, ...grpc.CallOption) (*rpc.Response, error) {
	in := llmtest.Context()
	b, _ := json.Marshal(in)
	output, _, e := (llm.Mock{}).Complete(context.Background(), llm.Call{Task: "insight", Scope: "company:company-test", Input: b})
	if e != nil {
		return nil, e
	}
	var card llm.Card
	_ = json.Unmarshal(output, &card)
	value := map[string]any{"context": in, "insight": llm.Insight{ID: "insight-test", CompanyID: "company-test", EventID: in.Prepared.Event.EventID, EventVersion: 2, ProfileVersion: 1, Card: card, Synthetic: true}}
	b, _ = json.Marshal(value)
	return &rpc.Response{Payload: b}, nil
}
func TestChatOwnershipOrderingAndIdempotency(t *testing.T) {
	ctx := context.Background()
	p := testdb.Open(t, "agent")
	if e := migrations.Apply(ctx, p); e != nil {
		t.Fatal(e)
	}
	s := Service{Pool: p, Client: pipeline.Client{Key: "01234567890123456789012345678901", Subject: "ai-agent-service"}, Insights: insightClient{}, Engine: llm.Engine{Pool: p, Provider: llm.Mock{}, DailyLimitMinor: 50000, MaxParallel: 4, Timeout: time.Second}}
	thread, e := s.CreateThread(ctx, "company-test", "user-one", "insight-test", "thread-key-1")
	if e != nil {
		t.Fatal(e)
	}
	again, e := s.CreateThread(ctx, "company-test", "user-one", "insight-test", "thread-key-1")
	if e != nil || thread.ID != again.ID {
		t.Fatal(again, e)
	}
	if _, e = s.GetThread(ctx, thread.ID, "company-test", "user-two", 20, 0); !errors.Is(e, pgx.ErrNoRows) {
		t.Fatal("same-company user accessed private thread", e)
	}
	if _, e = s.Send(ctx, thread.ID, "company-other", "user-one", "Что произошло?", "message-1"); !errors.Is(e, pgx.ErrNoRows) {
		t.Fatal("cross-company write", e)
	}
	first, e := s.Send(ctx, thread.ID, "company-test", "user-one", "Что произошло в новости?", "message-1")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Send(ctx, thread.ID, "company-test", "user-one", "Другой вопрос", "message-2"); !errors.Is(e, llm.ErrBusy) {
		t.Fatal("concurrent turn not blocked", e)
	}
	repeated, e := s.Send(ctx, thread.ID, "company-test", "user-one", "Что произошло в новости?", "message-1")
	if e != nil || repeated.ID != first.ID {
		t.Fatal(e)
	}
	if _, e = s.Jobs().Once(ctx, s.Handle, nil); e != nil {
		t.Fatal(e)
	}
	second, e := s.Send(ctx, thread.ID, "company-test", "user-one", "Неизвестная тема?", "message-2")
	if e != nil {
		t.Fatal(e)
	}
	var input ChatJob
	if llm.Strict(second.Input, &input) != nil || len(input.Input.History) != 2 {
		t.Fatal(string(second.Input))
	}
	if _, e = s.Jobs().Once(ctx, s.Handle, nil); e != nil {
		t.Fatal(e)
	}
	ready, e := s.Jobs().Get(ctx, second.ID, "company-test", "user-one")
	var turn Turn
	_ = json.Unmarshal(ready.Result, &turn)
	if e != nil || ready.Status != "ready" || !turn.Answer.InsufficientData {
		t.Fatal(ready, e)
	}
	history, e := s.GetThread(ctx, thread.ID, "company-test", "user-one", 20, 0)
	if e != nil || len(history.Messages) != 2 {
		t.Fatal(history, e)
	}
}
