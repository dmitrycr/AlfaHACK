package llm

import (
	"encoding/json"
	"strconv"
	"time"
)

type RuntimeConfig struct {
	Mode            string
	MockScenario    string
	Routing         Routing
	HTTP            HTTPConfig
	DailyLimitMinor int64
	MaxParallel     int
	Timeout         time.Duration
}

func (c RuntimeConfig) String() string   { return "RuntimeConfig(redacted)" }
func (c RuntimeConfig) GoString() string { return c.String() }
func LoadConfig(get func(string) string) (RuntimeConfig, error) {
	c := RuntimeConfig{Mode: "mock", MockScenario: "success", Routing: DefaultRouting(), DailyLimitMinor: 50000, MaxParallel: 4, Timeout: 3 * time.Second}
	value := func(key, fallback string) string {
		if s := get(key); s != "" {
			return s
		}
		return fallback
	}
	number := func(key string, fallback int64) (int64, error) {
		if s := get(key); s != "" {
			v, e := strconv.ParseInt(s, 10, 64)
			if e != nil || v < 0 {
				return 0, ErrInvalid
			}
			return v, nil
		}
		return fallback, nil
	}
	boolean := func(key string) (bool, error) {
		s := get(key)
		if s == "" {
			return false, nil
		}
		if s != "true" && s != "false" {
			return false, ErrInvalid
		}
		return s == "true", nil
	}
	c.Mode = value("LLM_MODE", "mock")
	c.MockScenario = value("LLM_MOCK_SCENARIO", "success")
	if !Contains([]string{"mock", "http"}, c.Mode) || !Contains([]string{"success", "invalid_json", "timeout", "rate_limit", "unavailable"}, c.MockScenario) {
		return c, ErrInvalid
	}
	var e error
	c.DailyLimitMinor, e = number("LLM_DAILY_LIMIT_MINOR", 50000)
	if e != nil || c.DailyLimitMinor < 1 || c.DailyLimitMinor > 50000 {
		return c, ErrInvalid
	}
	parallel, e := number("LLM_MAX_PARALLEL", 4)
	if e != nil || parallel < 1 || parallel > 16 {
		return c, ErrInvalid
	}
	c.MaxParallel = int(parallel)
	timeout, e := number("LLM_TIMEOUT_SECONDS", 3)
	if e != nil || timeout < 1 || timeout > 30 {
		return c, ErrInvalid
	}
	c.Timeout = time.Duration(timeout) * time.Second
	c.Routing.PriceUnitTokens, e = number("LLM_PRICE_UNIT_TOKENS", 0)
	if e != nil {
		return c, e
	}
	c.Routing.TariffsConfirmed, e = boolean("LLM_TARIFFS_CONFIRMED")
	if e != nil {
		return c, e
	}
	c.Routing.AllowKimi, e = boolean("LLM_ALLOW_KIMI")
	if e != nil {
		return c, e
	}
	for task, variable := range map[string]string{"tag": "LLM_MODEL_TAG", "profile": "LLM_MODEL_PROFILE", "insight": "LLM_MODEL_INSIGHT", "check": "LLM_MODEL_CHECK", "chat": "LLM_MODEL_CHAT"} {
		s := c.Routing.Agents[task]
		s.Model = value(variable, s.Model)
		c.Routing.Agents[task] = s
	}
	if raw := get("LLM_PRICES_JSON"); raw != "" {
		var prices map[string]Price
		if len(raw) > 8192 || Strict([]byte(raw), &prices) != nil {
			return c, ErrInvalid
		}
		c.Routing.Prices = prices
	}
	if c.Routing.Validate() != nil {
		return c, ErrInvalid
	}
	h := HTTPConfig{APIKey: get("LLM_API_KEY"), BaseURL: value("LLM_BASE_URL", PrimaryURL), FallbackURL: value("LLM_FALLBACK_BASE_URL", FallbackURL), KeyInfoURL: get("LLM_KEY_INFO_URL"), RemainingPath: get("LLM_KEY_INFO_REMAINING_PATH"), TokenLimitParameter: get("LLM_TOKEN_LIMIT_PARAMETER"), Routing: c.Routing, Timeout: c.Timeout, MaxParallel: c.MaxParallel, MaxResponseBytes: 1 << 20}
	h.PaidCallsEnabled, e = boolean("LLM_PAID_CALLS_ENABLED")
	if e != nil {
		return c, e
	}
	h.BudgetContractConfirmed, e = boolean("LLM_BUDGET_CONTRACT_CONFIRMED")
	if e != nil {
		return c, e
	}
	h.UsageContractConfirmed, e = boolean("LLM_USAGE_CONTRACT_CONFIRMED")
	if e != nil {
		return c, e
	}
	h.TokenLimitConfirmed, e = boolean("LLM_TOKEN_LIMIT_CONFIRMED")
	if e != nil {
		return c, e
	}
	c.HTTP = h
	c.HTTP.ConfigRevision = value("LLM_CONFIG_REVISION", "1")
	return c, nil
}
func (c RuntimeConfig) NewProvider() (Provider, func(), error) {
	if c.Mode == "mock" {
		return Mock{Scenario: c.MockScenario}, func() {}, nil
	}
	p, e := NewHTTPProvider(c.HTTP)
	if e != nil {
		return nil, func() {}, e
	}
	return p, p.Close, nil
}

// PriceJSON returns indicative configuration, never an assertion about live rates.
func (r Routing) PriceJSON() string { b, _ := json.Marshal(r.Prices); return string(b) }

type Preflight struct {
	Mode            string            `json:"mode"`
	KeyConfigured   bool              `json:"key_configured"`
	PaymentGateOpen bool              `json:"payment_gate_open"`
	Missing         []string          `json:"missing"`
	Models          map[string]string `json:"models"`
	PriceUnitTokens int64             `json:"price_unit_tokens"`
}

func (c RuntimeConfig) Preflight() Preflight {
	out := Preflight{Mode: c.Mode, KeyConfigured: c.HTTP.APIKey != "", Missing: []string{}, Models: map[string]string{}, PriceUnitTokens: c.Routing.PriceUnitTokens}
	for _, task := range []string{"tag", "profile", "insight", "check", "chat"} {
		out.Models[task] = c.Routing.Agents[task].Model
	}
	checks := []struct {
		ok   bool
		name string
	}{{c.Mode == "http", "http_mode"}, {out.KeyConfigured, "api_key"}, {c.HTTP.PaidCallsEnabled, "paid_calls_enabled"}, {c.Routing.TariffsConfirmed && c.Routing.PriceUnitTokens > 0, "confirmed_tariff_unit"}, {c.HTTP.BudgetContractConfirmed && allowedInfo(c.HTTP.KeyInfoURL) && c.HTTP.RemainingPath != "", "budget_contract"}, {c.HTTP.UsageContractConfirmed, "usage_contract"}, {c.HTTP.TokenLimitConfirmed && c.HTTP.TokenLimitParameter != "", "token_limit_contract"}}
	for _, v := range checks {
		if !v.ok {
			out.Missing = append(out.Missing, v.name)
		}
	}
	if len(out.Missing) == 0 {
		provider, e := NewHTTPProvider(c.HTTP)
		if e != nil {
			out.Missing = append(out.Missing, "provider_configuration")
		} else {
			if e = provider.gate(); e != nil {
				out.Missing = append(out.Missing, "provider_configuration")
			}
			provider.Close()
		}
	}
	out.PaymentGateOpen = len(out.Missing) == 0
	return out
}
