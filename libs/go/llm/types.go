// Package llm defines the backend-owned, versioned LLM contracts. No API accepts
// arbitrary system prompts, model names, URLs to fetch, or client company claims.
package llm

import (
	"alfa.local/shared/model"
	"encoding/json"
	"time"
)

const ContractVersion = "backend-llm-v1"
const VocabularyVersion = "ml-stage1-v1"

type Source struct {
	URL         string    `json:"url"`
	Name        string    `json:"name"`
	PublishedAt time.Time `json:"published_at"`
	Kind        string    `json:"kind"`
	FragmentID  string    `json:"fragment_id,omitempty"`
}

// ReadyEvent matches the ML v0.1 Event. Transport metadata lives outside it.
type ReadyEvent struct {
	EventID     string    `json:"event_id"`
	Title       string    `json:"title"`
	Summary     string    `json:"summary"`
	Sources     []Source  `json:"sources"`
	Type        string    `json:"type"`
	Sectors     []string  `json:"sectors"`
	OKVED       []string  `json:"okved"`
	Companies   []string  `json:"companies"`
	Regions     []string  `json:"regions"`
	Factors     []string  `json:"factors"`
	Direction   string    `json:"direction"`
	Horizon     string    `json:"horizon"`
	Importance  int       `json:"importance"`
	CollectedAt time.Time `json:"collected_at"`
}
type Prepared struct {
	WorkflowID        string     `json:"workflow_id"`
	SchemaVersion     string     `json:"schema_version"`
	EventVersion      int        `json:"event_version"`
	InputRevision     int        `json:"input_revision"`
	Scope             string     `json:"scope"`
	PromptVersion     string     `json:"prompt_version"`
	VocabularyVersion string     `json:"vocabulary_version"`
	ProviderVersion   string     `json:"provider_version"`
	Synthetic         bool       `json:"synthetic"`
	Event             ReadyEvent `json:"event"`
}
type TagInput struct {
	EventID       string    `json:"event_id"`
	InputRevision int       `json:"input_revision"`
	Title         string    `json:"title"`
	Text          string    `json:"text"`
	Sources       []Source  `json:"sources"`
	CollectedAt   time.Time `json:"collected_at"`
}
type TagResult struct {
	Status string      `json:"status"` // ready, noise, insufficient_data
	Reason string      `json:"reason"`
	Event  *ReadyEvent `json:"event"`
}
type Contribution struct {
	Channel      string  `json:"channel"`
	Weight       float64 `json:"weight"`
	Match        float64 `json:"match"`
	Contribution float64 `json:"contribution"`
}
type Match struct {
	CompanyID      string         `json:"company_id"`
	EventID        string         `json:"event_id"`
	ProfileVersion int            `json:"profile_version"`
	EventVersion   int            `json:"event_version"`
	RulesetVersion string         `json:"ruleset_version"`
	Score          float64        `json:"score"`
	Breakdown      []Contribution `json:"breakdown"`
	Synthetic      bool           `json:"synthetic"`
}
type Feature struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}
type BankExcerpt struct {
	ID        string `json:"id"`
	Source    Source `json:"source"`
	Text      string `json:"text"`
	Synthetic bool   `json:"synthetic"`
}
type InsightInput struct {
	Company         model.Company `json:"company"`
	Profile         model.Profile `json:"profile"`
	Prepared        Prepared      `json:"prepared"`
	Match           Match         `json:"match"`
	Features        []Feature     `json:"features"`
	Bank            []BankExcerpt `json:"bank"`
	BusinessProfile *ProfileBrief `json:"business_profile,omitempty"`
}
type ProfileInput struct {
	Company  model.Company `json:"company"`
	Profile  model.Profile `json:"profile"`
	Features []Feature     `json:"features"`
}
type ProfileObservation struct {
	Text       string   `json:"text"`
	FeatureIDs []string `json:"feature_ids"`
}
type ProfileBrief struct {
	CompanyID      string               `json:"company_id"`
	ProfileVersion int                  `json:"profile_version"`
	Summary        string               `json:"summary"`
	Observations   []ProfileObservation `json:"observations"`
	Hypotheses     []ProfileObservation `json:"hypotheses"`
	Limitations    []string             `json:"limitations"`
}
type Statement struct {
	Text            string   `json:"text"`
	Layer           string   `json:"layer"`
	Sources         []string `json:"sources"`
	ProfileFeatures []string `json:"profile_features"`
}
type Blocks struct {
	What         string `json:"what"`
	Why          string `json:"why"`
	Consequences string `json:"consequences"`
	Actions      string `json:"actions"`
	BankView     string `json:"bank_view"`
	Basis        string `json:"basis"`
}
type Card struct {
	Summary     string      `json:"summary"`
	Blocks      Blocks      `json:"blocks"`
	Statements  []Statement `json:"statements"`
	Confidence  string      `json:"confidence"`
	Limitations []string    `json:"limitations"`
}
type CheckInput struct {
	Context InsightInput `json:"context"`
	Card    Card         `json:"card"`
}
type CheckResult struct {
	Passed  bool     `json:"passed"`
	Reasons []string `json:"reasons"`
}
type Insight struct {
	ID              string        `json:"id"`
	CompanyID       string        `json:"company_id"`
	EventID         string        `json:"event_id"`
	EventVersion    int           `json:"event_version"`
	ProfileVersion  int           `json:"profile_version"`
	RulesetVersion  string        `json:"ruleset_version"`
	RunID           string        `json:"run_id"`
	CheckRunID      string        `json:"check_run_id"`
	Sources         []Source      `json:"sources"`
	ProfileFeatures []Feature     `json:"profile_features"`
	WorkflowID      string        `json:"workflow_id"`
	ProfileRunID    string        `json:"profile_run_id"`
	BusinessProfile *ProfileBrief `json:"business_profile,omitempty"`
	Synthetic       bool          `json:"synthetic"`
	Card            Card          `json:"card"`
	Check           CheckResult   `json:"check"`
}
type Message struct {
	Role string `json:"role"`
	Text string `json:"text"`
}
type ChatInput struct {
	Context  InsightInput `json:"context"`
	Insight  Insight      `json:"insight"`
	History  []Message    `json:"history"`
	Question string       `json:"question"`
}
type Answer struct {
	Text             string   `json:"text"`
	Sources          []string `json:"sources"`
	InsufficientData bool     `json:"insufficient_data"`
	Limitations      []string `json:"limitations"`
}
type Call struct {
	Task  string          `json:"task"`
	Scope string          `json:"scope"`
	Input json.RawMessage `json:"input"`
}
type Completion struct {
	RunID           string          `json:"run_id"`
	Output          json.RawMessage `json:"output"`
	Cached          bool            `json:"cached"`
	PromptVersion   string          `json:"prompt_version"`
	ProviderVersion string          `json:"provider_version"`
	Synthetic       bool            `json:"synthetic"`
	WorkflowID      string          `json:"workflow_id"`
	Check           *CheckResult    `json:"check,omitempty"`
	CheckRunID      string          `json:"check_run_id,omitempty"`
	BusinessProfile *ProfileBrief   `json:"business_profile,omitempty"`
	ProfileRunID    string          `json:"profile_run_id,omitempty"`
}
type Trace struct {
	RunID           string `json:"run_id"`
	Task            string `json:"task"`
	Status          string `json:"status"`
	Model           string `json:"model"`
	PromptVersion   string `json:"prompt_version"`
	InputHash       string `json:"input_hash"`
	TokensIn        int    `json:"tokens_in"`
	TokensOut       int    `json:"tokens_out"`
	CostMinor       int64  `json:"cost_minor"`
	CostNano        int64  `json:"cost_nano_rub"`
	ReservedNano    int64  `json:"reserved_nano_rub"`
	CostEstimated   bool   `json:"cost_estimated"`
	ReasoningTokens int    `json:"reasoning_tokens"`
	Synthetic       bool   `json:"synthetic"`
	DurationMS      int64  `json:"duration_ms"`
	ErrorCode       string `json:"error_code"`
}
