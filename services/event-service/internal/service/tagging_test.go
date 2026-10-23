package service

import (
	"alfa.local/shared/jobs"
	"alfa.local/shared/llm"
	"alfa.local/shared/llm/llmtest"
	"alfa.local/shared/platform"
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestPreparedPublicationFencesStaleAndFailedJobs(t *testing.T) {
	s := setup(t)
	ctx := context.Background()
	input := llmtest.Tag()
	a := article("published", "public", "hash-new", 1)
	a.Title = input.Title
	a.Text = input.Text
	a.URL = input.Sources[0].URL
	a.PublishedAt = &input.Sources[0].PublishedAt
	event := apply(t, s, a)
	in := input
	in.EventID = event.ID
	in.CollectedAt = event.CreatedAt
	b, _ := json.Marshal(in)
	output, _, e := (llm.Mock{}).Complete(ctx, llm.Call{Task: "tag", Scope: "public", Input: b})
	if e != nil {
		t.Fatal(e)
	}
	completion, _ := json.Marshal(llm.Completion{Output: output, ProviderVersion: "mock-v1", PromptVersion: llm.PromptVersion("tag"), Synthetic: true})
	worker := TagWorker{Store: s}
	q := jobs.Store{Pool: s.Pool, Schema: "events"}
	if _, e = q.Once(ctx, func(context.Context, jobs.Job) (json.RawMessage, error) { return completion, nil }, worker.Commit); e != nil {
		t.Fatal(e)
	}
	ready, e := s.Prepared(ctx, event.ID, "company-metal")
	if e != nil || ready.EventVersion != 2 || ready.Event.EventID != event.ID {
		t.Fatal(ready, e)
	}
	a.Revision++
	a.Text += " Изменено."
	newer := apply(t, s, a)
	if _, e = s.Prepared(ctx, event.ID, "company-metal"); e == nil {
		t.Fatal("stale prepared result still served")
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	jobInput, _ := json.Marshal(event)
	e = worker.Commit(ctx, tx, jobs.Job{Input: jobInput}, completion)
	_ = tx.Rollback(ctx)
	if !errors.Is(e, jobs.ErrObsolete) {
		t.Fatal("late output accepted", e)
	}
	// An exhausted old job must not fail a newer version.
	if _, e = s.Pool.Exec(ctx, "UPDATE events.jobs SET status='failed' WHERE input->>'id'=$1 AND (input->>'version')::integer=$2", event.ID, event.Version); e != nil {
		t.Fatal(e)
	}
	if e = s.ReconcileFailures(ctx); e != nil {
		t.Fatal(e)
	}
	current, e := s.Get(ctx, event.ID, "company-metal")
	if e != nil || current.Version != newer.Version || current.Status != "awaiting_tagging" {
		t.Fatal(current, e)
	}
	if _, e = s.Pool.Exec(ctx, "UPDATE events.jobs SET status='failed' WHERE input->>'id'=$1", event.ID); e != nil {
		t.Fatal(e)
	}
	if e = s.ReconcileFailures(ctx); e != nil {
		t.Fatal(e)
	}
	current, e = s.Get(ctx, event.ID, "company-metal")
	if e != nil || current.Status != "tagging_failed" {
		t.Fatal(current, e)
	}
	var count int
	if e = s.Pool.QueryRow(ctx, "SELECT COUNT(*) FROM events.outbox WHERE payload->>'type'='event.tagging_failed'").Scan(&count); e != nil || count != 1 {
		t.Fatal(count, e)
	}
}
func TestMissingSourceNeverCreatesPreparedEvent(t *testing.T) {
	s := setup(t)
	ctx := context.Background()
	event := apply(t, s, article("missing", "company:company-metal", platform.Hash("empty"), 1))
	output, _ := json.Marshal(llm.TagResult{Status: "insufficient_data", Reason: "Нет источника с датой."})
	completion, _ := json.Marshal(llm.Completion{Output: output, ProviderVersion: "mock-v1", Synthetic: true})
	worker := TagWorker{Store: s}
	q := jobs.Store{Pool: s.Pool, Schema: "events"}
	if _, e := q.Once(ctx, func(context.Context, jobs.Job) (json.RawMessage, error) { return completion, nil }, worker.Commit); e != nil {
		t.Fatal(e)
	}
	got, e := s.Get(ctx, event.ID, "company-metal")
	if e != nil || got.Status != "insufficient_data" {
		t.Fatal(got, e)
	}
	if _, e = s.Prepared(ctx, event.ID, "company-metal"); e == nil {
		t.Fatal("missing provenance accepted")
	}
}
