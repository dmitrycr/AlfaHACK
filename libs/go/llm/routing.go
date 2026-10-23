package llm

import (
	"encoding/json"
	"math/big"
	"strings"
	"time"
)

const NanoPerMinor int64 = 10_000_000
const GLM = "z-ai/glm-5.3-flash"
const Gemma = "google/gemma-4-26b-a4b-it"
const DeepSeek = "deepseek/deepseek-v4.1-flash"
const Qwen = "qwen/qwen3.6-35b-a3b"
const Kimi = "moonshotai/kimi-k2.6"

var TextModels = []string{GLM, Gemma, DeepSeek, Qwen, Kimi}

// Prices are indicative values from the supplied guide. Unit=0 deliberately
// prevents real billing until the organizers confirm the tariff denominator.
type Price struct {
	Input  string `json:"input_rub"`
	Output string `json:"output_rub"`
}
type AgentSpec struct {
	Model           string `json:"model"`
	MaxInputTokens  int    `json:"max_input_tokens"`
	MaxOutputTokens int    `json:"max_output_tokens"`
}
type Routing struct {
	Agents           map[string]AgentSpec `json:"agents"`
	Prices           map[string]Price     `json:"prices"`
	PriceUnitTokens  int64                `json:"price_unit_tokens"`
	TariffsConfirmed bool                 `json:"tariffs_confirmed"`
	AllowKimi        bool                 `json:"allow_kimi"`
}

func DefaultRouting() Routing {
	return Routing{Agents: map[string]AgentSpec{
		"tag": {GLM, 32000, 2000}, "profile": {DeepSeek, 32000, 1800}, "insight": {Qwen, 32000, 3000}, "check": {Gemma, 32000, 800}, "chat": {GLM, 32000, 1800}, "final": {Kimi, 32000, 3000},
	}, Prices: map[string]Price{GLM: {"5.10", "17.00"}, Gemma: {"4.48", "23.00"}, DeepSeek: {"10.00", "21.00"}, Qwen: {"5.33", "74.00"}, Kimi: {"51.00", "270.00"}}}
}
func (r Routing) spec(task string) (AgentSpec, error) {
	s, ok := r.Agents[task]
	if !ok || !Contains(TextModels, s.Model) || s.MaxInputTokens < 256 || s.MaxInputTokens > 131072 || s.MaxOutputTokens < 64 || s.MaxOutputTokens > 16384 {
		return s, ErrInvalid
	}
	if s.Model == Kimi && (!r.AllowKimi || task != "final") {
		return s, &ProviderError{Code: "kimi_disabled"}
	}
	return s, nil
}
func (r Routing) Validate() error {
	for _, task := range []string{"tag", "profile", "insight", "check", "chat"} {
		if _, e := r.spec(task); e != nil {
			return e
		}
	}
	if r.PriceUnitTokens != 0 && r.PriceUnitTokens != 1000 && r.PriceUnitTokens != 1_000_000 {
		return ErrInvalid
	}
	for model, p := range r.Prices {
		if !Contains(TextModels, model) {
			return ErrInvalid
		}
		if _, e := p.Cost(1, 1, 1_000_000); e != nil {
			return e
		}
	}
	return nil
}

// Cost uses rational arithmetic and rounds up once at nanoruble precision.
func (p Price) Cost(input, output int, unit int64) (int64, error) {
	if input < 0 || output < 0 || input > 2_000_000 || output > 2_000_000 || (unit != 1000 && unit != 1_000_000) {
		return 0, ErrInvalid
	}
	a, ok := new(big.Rat).SetString(p.Input)
	if !ok || a.Sign() < 0 {
		return 0, ErrInvalid
	}
	b, ok := new(big.Rat).SetString(p.Output)
	if !ok || b.Sign() < 0 {
		return 0, ErrInvalid
	}
	a.Mul(a, new(big.Rat).SetInt64(int64(input)))
	b.Mul(b, new(big.Rat).SetInt64(int64(output)))
	a.Add(a, b)
	a.Mul(a, big.NewRat(1_000_000_000, unit))
	q, rem := new(big.Int), new(big.Int)
	q.QuoRem(a.Num(), a.Denom(), rem)
	if rem.Sign() > 0 {
		q.Add(q, big.NewInt(1))
	}
	if !q.IsInt64() {
		return 0, ErrInvalid
	}
	return q.Int64(), nil
}
func RubToNano(s string) (int64, error) {
	return rubAmount(s)
}

// A ruble amount is independent of a token-price denominator.
func rubAmount(s string) (int64, error) {
	if len(s) > 40 || strings.ContainsAny(s, "eE/") {
		return 0, ErrInvalid
	}
	v, ok := new(big.Rat).SetString(s)
	if !ok || v.Sign() < 0 {
		return 0, ErrInvalid
	}
	v.Mul(v, big.NewRat(1_000_000_000, 1))
	q, rem := new(big.Int), new(big.Int)
	q.QuoRem(v.Num(), v.Denom(), rem)
	if rem.Sign() != 0 || !q.IsInt64() {
		return 0, ErrInvalid
	}
	return q.Int64(), nil
}

type ProviderError struct {
	Code      string
	Retryable bool
	Ambiguous bool
	Wait      time.Duration
}

func (e *ProviderError) Error() string             { return e.Code }
func (e *ProviderError) JobCode() string           { return e.Code }
func (e *ProviderError) Terminal() bool            { return !e.Retryable || e.Ambiguous }
func (e *ProviderError) Uncertain() bool           { return e.Ambiguous }
func (e *ProviderError) RetryDelay() time.Duration { return e.Wait }
func (e *ProviderError) Is(target error) bool {
	return (target == ErrBudget && e.Code == "llm_budget_exceeded") || (target == ErrInvalid && (e.Code == "invalid_model_output" || e.Code == "invalid_llm_contract"))
}
func canonical(v any) []byte { b, _ := json.Marshal(v); return b }
