package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

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
	Engine    llm.Engine
	Client    pipeline.Client
	Insights  rpc.InsightServiceClient
	Company   rpc.CompanyServiceClient
	AllowKimi bool
}
type Thread struct {
	ID        string     `json:"id"`
	CompanyID string     `json:"company_id"`
	InsightID string     `json:"insight_id"`
	CreatedAt time.Time  `json:"created_at"`
	Messages  []jobs.Job `json:"messages"`
}
type ChatJob struct {
	ThreadID string        `json:"thread_id"`
	Input    llm.ChatInput `json:"input"`
}
type Turn struct {
	ThreadID   string     `json:"thread_id"`
	Question   string     `json:"question"`
	Answer     llm.Answer `json:"answer"`
	RunID      string     `json:"run_id"`
	Synthetic  bool       `json:"synthetic"`
	WorkflowID string     `json:"workflow_id"`
}

func (s Service) Jobs() jobs.Store { return jobs.Store{Pool: s.Pool, Schema: "agent"} }
func (s Service) CreateThread(ctx context.Context, company, user, insight, key string) (Thread, error) {
	if !pipeline.ValidKey(key) || insight == "" || len(insight) > 128 || user == "" {
		return Thread{}, llm.ErrInvalid
	}
	existing, e := s.threadByKey(ctx, company, user, key)
	if e == nil {
		if existing.InsightID != insight {
			return Thread{}, jobs.ErrConflict
		}
		return existing, nil
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return Thread{}, e
	}
	var data struct {
		Context llm.InsightInput `json:"context"`
		Insight llm.Insight      `json:"insight"`
	}
	if e = s.Client.Call(ctx, "insight-service", company, insight, nil, s.Insights.GetInsightContext, &data); e != nil {
		return Thread{}, e
	}
	if llm.ValidateInsightInput(data.Context) != nil || llm.ValidateCard(data.Context, data.Insight.Card) != nil || data.Insight.CompanyID != company {
		return Thread{}, llm.ErrInvalid
	}
	input, _ := json.Marshal(data.Context)
	card, _ := json.Marshal(data.Insight)
	_, e = s.Pool.Exec(ctx, `INSERT INTO agent.threads(id,company_id,user_id,insight_id,idempotency_key,context,insight) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(company_id,user_id,idempotency_key) DO NOTHING`, platform.ID(), company, user, insight, key, input, card)
	if e != nil {
		return Thread{}, e
	}
	existing, e = s.threadByKey(ctx, company, user, key)
	if e == nil && existing.InsightID != insight {
		return Thread{}, jobs.ErrConflict
	}
	return existing, e
}
func (s Service) threadByKey(ctx context.Context, company, user, key string) (Thread, error) {
	out := Thread{Messages: []jobs.Job{}}
	e := s.Pool.QueryRow(ctx, "SELECT id,company_id,insight_id,created_at FROM agent.threads WHERE company_id=$1 AND user_id=$2 AND idempotency_key=$3", company, user, key).Scan(&out.ID, &out.CompanyID, &out.InsightID, &out.CreatedAt)
	return out, e
}
func (s Service) GetThread(ctx context.Context, id, company, user string, limit, offset int) (Thread, error) {
	out := Thread{Messages: []jobs.Job{}}
	e := s.Pool.QueryRow(ctx, "SELECT id,company_id,insight_id,created_at FROM agent.threads WHERE id=$1 AND company_id=$2 AND user_id=$3", id, company, user).Scan(&out.ID, &out.CompanyID, &out.InsightID, &out.CreatedAt)
	if e != nil {
		return out, e
	}
	rows, e := s.Pool.Query(ctx, "SELECT id FROM agent.jobs WHERE kind='chat' AND company_id=$1 AND user_id=$2 AND input->>'thread_id'=$3 ORDER BY created_at,id LIMIT $4 OFFSET $5", company, user, id, limit, offset)
	if e != nil {
		return out, e
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return out, e
		}
		ids = append(ids, id)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return out, e
	}
	for _, id := range ids {
		j, e := s.Jobs().Get(ctx, id, company, user)
		if e != nil {
			return out, e
		}
		out.Messages = append(out.Messages, j)
	}
	return out, nil
}
func (s Service) Send(ctx context.Context, id, company, user, question, key string) (jobs.Job, error) {
	if !pipeline.ValidKey(key) || strings.TrimSpace(question) == "" || utf8.RuneCountInString(question) > 4000 {
		return jobs.Job{}, llm.ErrInvalid
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return jobs.Job{}, e
	}
	defer tx.Rollback(ctx)
	var contextJSON, insightJSON []byte
	e = tx.QueryRow(ctx, "SELECT context,insight FROM agent.threads WHERE id=$1 AND company_id=$2 AND user_id=$3 FOR UPDATE", id, company, user).Scan(&contextJSON, &insightJSON)
	if e != nil {
		return jobs.Job{}, e
	}
	hash := platform.Hash(question)
	composite := id + ":" + key
	var jobID, stored string
	e = tx.QueryRow(ctx, "SELECT id,request_hash FROM agent.jobs WHERE kind='chat' AND company_id=$1 AND user_id=$2 AND idempotency_key=$3", company, user, composite).Scan(&jobID, &stored)
	if e == nil {
		if hash != stored {
			return jobs.Job{}, jobs.ErrConflict
		}
		j, e := jobs.GetFrom(ctx, tx, "agent", jobID, company, user)
		return j, e
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return jobs.Job{}, e
	}
	var busy bool
	if e = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM agent.jobs WHERE kind='chat' AND company_id=$1 AND user_id=$2 AND input->>'thread_id'=$3 AND status IN ('pending','running','retry'))", company, user, id).Scan(&busy); e != nil {
		return jobs.Job{}, e
	}
	if busy {
		return jobs.Job{}, llm.ErrBusy
	}
	in := llm.ChatInput{Question: question, History: []llm.Message{}}
	if llm.Strict(contextJSON, &in.Context) != nil || llm.Strict(insightJSON, &in.Insight) != nil {
		return jobs.Job{}, llm.ErrInvalid
	}
	rows, e := tx.Query(ctx, "SELECT result FROM agent.jobs WHERE kind='chat' AND company_id=$1 AND user_id=$2 AND input->>'thread_id'=$3 AND status='ready' ORDER BY created_at DESC,id DESC LIMIT 6", company, user, id)
	if e != nil {
		return jobs.Job{}, e
	}
	turns := []Turn{}
	for rows.Next() {
		var b []byte
		var turn Turn
		if e = rows.Scan(&b); e != nil {
			rows.Close()
			return jobs.Job{}, e
		}
		if llm.Strict(b, &turn) != nil {
			rows.Close()
			return jobs.Job{}, llm.ErrInvalid
		}
		turns = append(turns, turn)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return jobs.Job{}, e
	}
	for i := len(turns) - 1; i >= 0; i-- {
		in.History = append(in.History, llm.Message{Role: "user", Text: turns[i].Question}, llm.Message{Role: "assistant", Text: turns[i].Answer.Text})
	}
	if llm.ValidateChat(in) != nil {
		return jobs.Job{}, llm.ErrInvalid
	}
	b, _ := json.Marshal(ChatJob{ThreadID: id, Input: in})
	j, e := jobs.CreateRaw(ctx, tx, "agent", "chat", company, user, composite, hash, b)
	if e != nil {
		return j, e
	}
	return j, tx.Commit(ctx)
}
func (s Service) Handle(ctx context.Context, j jobs.Job) (json.RawMessage, error) {
	if j.Kind == "profile" {
		var in llm.ProfileInput
		if llm.Strict(j.Input, &in) != nil || in.Company.ID != j.CompanyID {
			return nil, llm.ErrInvalid
		}
		out, e := s.Orchestrator().Run(ctx, llm.Call{Task: "profile", Scope: "company:" + j.CompanyID, Input: j.Input})
		if e != nil {
			return nil, e
		}
		return json.Marshal(out)
	}
	var in ChatJob
	if llm.Strict(j.Input, &in) != nil || llm.ValidateChat(in.Input) != nil || in.Input.Context.Company.ID != j.CompanyID {
		return nil, llm.ErrInvalid
	}
	b, _ := json.Marshal(in.Input)
	result, e := s.Orchestrator().Run(ctx, llm.Call{Task: "chat", Scope: "company:" + j.CompanyID, Input: b})
	if e != nil {
		return nil, e
	}
	var answer llm.Answer
	if llm.Strict(result.Output, &answer) != nil {
		return nil, llm.ErrInvalid
	}
	return json.Marshal(Turn{ThreadID: in.ThreadID, Question: in.Input.Question, Answer: answer, RunID: result.RunID, Synthetic: result.Synthetic, WorkflowID: result.WorkflowID})
}
func (s Service) Orchestrator() llm.Orchestrator {
	return llm.Orchestrator{Engine: s.Engine, AllowKimi: s.AllowKimi}
}
func (s Service) QueueProfile(ctx context.Context, company string) (jobs.Job, error) {
	var in llm.ProfileInput
	if e := s.Client.Call(ctx, "company-service", company, "", nil, s.Company.GetCompany, &in.Company); e != nil {
		return jobs.Job{}, e
	}
	if e := s.Client.Call(ctx, "company-service", company, "", nil, s.Company.GetProfile, &in.Profile); e != nil {
		return jobs.Job{}, e
	}
	in.Features = llm.FeaturesFor(in.Company, in.Profile)
	if llm.ValidateProfileInput(in) != nil {
		return jobs.Job{}, llm.ErrInvalid
	}
	hash := platform.Hash(string(canonicalInput(in)) + s.Engine.Provider.Version() + string(canonicalInput(s.Engine.Routing)))
	if s.Engine.Provider.Version() != "mock-v1" {
		hash = platform.Hash(hash + time.Now().UTC().Format("2006-01-02"))
	}
	return jobs.Create(ctx, s.Pool, "agent", "profile", company, "", hash, in)
}
func canonicalInput(v any) []byte { b, _ := json.Marshal(v); return b }
