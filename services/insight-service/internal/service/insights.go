package service

import (
	"context"
	"encoding/json"
	"errors"

	"alfa.local/shared/jobs"
	"alfa.local/shared/llm"
	"alfa.local/shared/pipeline"
	"alfa.local/shared/platform"
	"alfa.local/shared/rpc"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Service struct {
	Pool      *pgxpool.Pool
	Client    pipeline.Client
	Company   rpc.CompanyServiceClient
	Events    rpc.EventServiceClient
	AI        rpc.AIServiceClient
	Knowledge llm.Knowledge
	MatchMode string
}

func (s Service) Jobs() jobs.Store { return jobs.Store{Pool: s.Pool, Schema: "insights"} }
func (s Service) Snapshot(ctx context.Context, company, event string) (llm.InsightInput, error) {
	in := llm.InsightInput{Bank: []llm.BankExcerpt{}, Features: []llm.Feature{}}
	if e := s.Client.Call(ctx, "company-service", company, "", nil, s.Company.GetCompany, &in.Company); e != nil {
		return in, e
	}
	if e := s.Client.Call(ctx, "company-service", company, "", nil, s.Company.GetProfile, &in.Profile); e != nil {
		return in, e
	}
	if e := s.Client.Call(ctx, "event-service", company, event, nil, s.Events.GetPreparedEvent, &in.Prepared); e != nil {
		return in, e
	}
	in.Features = llm.FeaturesFor(in.Company, in.Profile)
	return in, nil
}
func (s Service) StoreMatch(ctx context.Context, m llm.Match) error {
	if llm.ValidateMatch(m) != nil || m.Synthetic {
		return llm.ErrInvalid
	}
	in, e := s.Snapshot(ctx, m.CompanyID, m.EventID)
	if e != nil {
		return e
	}
	if m.ProfileVersion != in.Profile.Version || m.EventVersion != in.Prepared.EventVersion {
		return jobs.ErrObsolete
	}
	b, _ := json.Marshal(m)
	_, e = s.Pool.Exec(ctx, `INSERT INTO insights.matches(company_id,event_id,profile_version,event_version,document) VALUES($1,$2,$3,$4,$5) ON CONFLICT(company_id,event_id,profile_version,event_version) DO UPDATE SET document=excluded.document`, m.CompanyID, m.EventID, m.ProfileVersion, m.EventVersion, b)
	return e
}
func (s Service) Create(ctx context.Context, company, event, key string) (jobs.Job, error) {
	if event == "" || len(event) > 128 || !pipeline.ValidKey(key) {
		return jobs.Job{}, llm.ErrInvalid
	}
	hash := platform.Hash(event)
	// Resolve retries before fetching mutable upstream snapshots.
	var id, stored string
	e := s.Pool.QueryRow(ctx, "SELECT id,request_hash FROM insights.jobs WHERE kind='insight' AND company_id=$1 AND user_id='' AND idempotency_key=$2", company, key).Scan(&id, &stored)
	if e == nil {
		if stored != hash {
			return jobs.Job{}, jobs.ErrConflict
		}
		return s.Jobs().Get(ctx, id, company, "")
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return jobs.Job{}, e
	}
	in, e := s.Snapshot(ctx, company, event)
	if e != nil {
		return jobs.Job{}, e
	}
	var b []byte
	e = s.Pool.QueryRow(ctx, "SELECT document FROM insights.matches WHERE company_id=$1 AND event_id=$2 AND profile_version=$3 AND event_version=$4", company, event, in.Profile.Version, in.Prepared.EventVersion).Scan(&b)
	if e == nil {
		if llm.Strict(b, &in.Match) != nil {
			return jobs.Job{}, llm.ErrInvalid
		}
	} else if errors.Is(e, pgx.ErrNoRows) && s.MatchMode == "mock" {
		in.Match = llm.Match{CompanyID: company, EventID: event, ProfileVersion: in.Profile.Version, EventVersion: in.Prepared.EventVersion, RulesetVersion: "mock-match-v1", Score: 0.5, Breakdown: []llm.Contribution{{Channel: "industry", Weight: 1, Match: 0.5, Contribution: 0.5}}, Synthetic: true}
	} else if errors.Is(e, pgx.ErrNoRows) {
		return jobs.Job{}, jobs.ErrObsolete
	} else {
		return jobs.Job{}, e
	}
	if in.Match.Score <= 0 {
		return jobs.Job{}, &llm.ProviderError{Code: "event_not_selected"}
	}
	if s.Knowledge == nil {
		return jobs.Job{}, llm.ErrInvalid
	}
	in.Bank, e = s.Knowledge.Search(ctx, in.Prepared.Event)
	if e != nil {
		return jobs.Job{}, e
	}
	if llm.ValidateInsightInput(in) != nil {
		return jobs.Job{}, llm.ErrInvalid
	}
	input, _ := json.Marshal(in)
	return jobs.CreateRaw(ctx, s.Pool, "insights", "insight", company, "", key, hash, input)
}
func (s Service) Handle(ctx context.Context, j jobs.Job) (json.RawMessage, error) {
	var in llm.InsightInput
	if llm.Strict(j.Input, &in) != nil || llm.ValidateInsightInput(in) != nil {
		return nil, llm.ErrInvalid
	}
	// Pin the queued versions. A later profile/news change requires a new job.
	current, e := s.Snapshot(ctx, j.CompanyID, in.Prepared.Event.EventID)
	if e != nil {
		return nil, e
	}
	if current.Profile.Version != in.Profile.Version || current.Prepared.EventVersion != in.Prepared.EventVersion {
		return nil, jobs.ErrObsolete
	}
	generated, e := s.Client.Generate(ctx, j.CompanyID, "company:"+j.CompanyID, "insight", in, s.AI)
	if e != nil {
		return nil, e
	}
	var card llm.Card
	if llm.Strict(generated.Output, &card) != nil || llm.ValidateCard(in, card) != nil {
		return nil, llm.ErrInvalid
	}
	if generated.Check == nil || !generated.Check.Passed || generated.BusinessProfile == nil || generated.CheckRunID == "" || generated.ProfileRunID == "" {
		return nil, llm.ErrInvalid
	}
	if llm.ValidateProfileBrief(llm.ProfileInput{Company: in.Company, Profile: in.Profile, Features: in.Features}, *generated.BusinessProfile) != nil {
		return nil, llm.ErrInvalid
	}
	check := *generated.Check
	// Recheck after both model operations to reject results already stale at publication.
	current, e = s.Snapshot(ctx, j.CompanyID, in.Prepared.Event.EventID)
	if e != nil {
		return nil, e
	}
	if current.Profile.Version != in.Profile.Version || current.Prepared.EventVersion != in.Prepared.EventVersion {
		return nil, jobs.ErrObsolete
	}
	synthetic := generated.Synthetic || in.Match.Synthetic || in.Profile.Synthetic || in.Prepared.Synthetic
	for _, b := range in.Bank {
		synthetic = synthetic || b.Synthetic
	}
	result := llm.Insight{ID: j.ID, CompanyID: j.CompanyID, EventID: in.Prepared.Event.EventID, EventVersion: in.Prepared.EventVersion, ProfileVersion: in.Profile.Version, RulesetVersion: in.Match.RulesetVersion, RunID: generated.RunID, CheckRunID: generated.CheckRunID, Synthetic: synthetic, Card: card, Check: check}
	result.WorkflowID = generated.WorkflowID
	result.ProfileRunID = generated.ProfileRunID
	result.BusinessProfile = generated.BusinessProfile
	result.Sources = append([]llm.Source{}, in.Prepared.Event.Sources...)
	for _, b := range in.Bank {
		result.Sources = append(result.Sources, b.Source)
	}
	result.ProfileFeatures = append([]llm.Feature{}, in.Features...)
	return json.Marshal(result)
}
func (s Service) Context(ctx context.Context, id, company string) (llm.InsightInput, llm.Insight, error) {
	j, e := s.Jobs().Get(ctx, id, company, "")
	var in llm.InsightInput
	var out llm.Insight
	if e != nil {
		return in, out, e
	}
	if j.Status != "ready" {
		return in, out, jobs.ErrObsolete
	}
	if llm.Strict(j.Input, &in) != nil || llm.Strict(j.Result, &out) != nil {
		return in, out, llm.ErrInvalid
	}
	return in, out, nil
}
