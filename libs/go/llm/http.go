package llm

import (
	"alfa.local/shared/platform"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

const PrimaryURL = "https://changellenge.up.railway.app/v1"
const FallbackURL = "https://api.alfahack-gateway26.ru/v1"

type HTTPConfig struct {
	ConfigRevision          string
	BaseURL                 string
	FallbackURL             string
	APIKey                  string `json:"-"`
	KeyInfoURL              string
	RemainingPath           string
	BudgetContractConfirmed bool
	UsageContractConfirmed  bool
	TokenLimitParameter     string
	TokenLimitConfirmed     bool
	PaidCallsEnabled        bool
	Routing                 Routing
	Timeout                 time.Duration
	MaxParallel             int
	MaxResponseBytes        int64
}
type BudgetSnapshot struct {
	RemainingNano int64
	StartedAt     time.Time
}
type managedProvider interface {
	Spec(string) (AgentSpec, error)
	IsSynthetic() bool
	Reserve(Call) (int64, error)
	Cost(string, Usage) (int64, error)
	Budget(context.Context) (BudgetSnapshot, error)
}
type HTTPProvider struct {
	cfg         HTTPConfig
	client      *http.Client
	transport   *http.Transport
	slots       chan struct{}
	fingerprint string
}

func allowedBase(s string) bool { return s == PrimaryURL || s == FallbackURL }
func allowedInfo(s string) bool {
	u, e := url.Parse(s)
	if e != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Scheme != "https" {
		return false
	}
	return (u.Host == "changellenge.up.railway.app" || u.Host == "api.alfahack-gateway26.ru") && (u.Path == "/key/info" || u.Path == "/v1/key/info")
}
func NewHTTPProvider(cfg HTTPConfig) (*HTTPProvider, error) { return newHTTPProvider(cfg, nil) }
func newHTTPProvider(cfg HTTPConfig, rt http.RoundTripper) (*HTTPProvider, error) {
	if !allowedBase(cfg.BaseURL) || (cfg.FallbackURL != "" && (!allowedBase(cfg.FallbackURL) || cfg.FallbackURL == cfg.BaseURL)) || strings.TrimSpace(cfg.APIKey) == "" || strings.ContainsAny(cfg.APIKey, "\r\n") || len(cfg.APIKey) > 4096 || cfg.Timeout < time.Millisecond || cfg.Timeout > 30*time.Second || cfg.MaxParallel < 1 || cfg.MaxParallel > 16 || cfg.MaxResponseBytes < 1024 || cfg.MaxResponseBytes > 2<<20 || cfg.Routing.Validate() != nil {
		return nil, ErrInvalid
	}
	if cfg.KeyInfoURL != "" && !allowedInfo(cfg.KeyInfoURL) {
		return nil, ErrInvalid
	}
	if cfg.TokenLimitParameter != "" && !Contains([]string{"max_tokens", "max_completion_tokens"}, cfg.TokenLimitParameter) {
		return nil, ErrInvalid
	}
	var frozen Routing
	if json.Unmarshal(canonical(cfg.Routing), &frozen) != nil {
		return nil, ErrInvalid
	}
	cfg.Routing = frozen
	p := &HTTPProvider{cfg: cfg, slots: make(chan struct{}, cfg.MaxParallel)}
	if rt == nil {
		p.transport = &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: cfg.Timeout, IdleConnTimeout: 30 * time.Second, MaxIdleConns: cfg.MaxParallel * 2, MaxIdleConnsPerHost: cfg.MaxParallel, MaxConnsPerHost: cfg.MaxParallel, MaxResponseHeaderBytes: 32 << 10, DisableCompression: true}
		rt = p.transport
	}
	p.client = &http.Client{Transport: rt, Timeout: cfg.Timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	// No API key in identity, errors, traces or printable configuration.
	identity := struct {
		Base, Fallback, Info, Path, TokenParam, Revision string
		Routing                                          Routing
	}{cfg.BaseURL, cfg.FallbackURL, cfg.KeyInfoURL, cfg.RemainingPath, cfg.TokenLimitParameter, cfg.ConfigRevision, cfg.Routing}
	p.fingerprint = "http-v1:" + hashBytes(append(canonical(identity), canonical([]bool{cfg.PaidCallsEnabled, cfg.BudgetContractConfirmed, cfg.UsageContractConfirmed, cfg.TokenLimitConfirmed})...))
	return p, nil
}
func hashBytes(b []byte) string                             { return platform.Hash(string(b)) }
func (c HTTPConfig) String() string                         { return "HTTPConfig(redacted)" }
func (c HTTPConfig) GoString() string                       { return c.String() }
func (p *HTTPProvider) Version() string                     { return p.fingerprint }
func (p *HTTPProvider) IsSynthetic() bool                   { return false }
func (p *HTTPProvider) Spec(task string) (AgentSpec, error) { return p.cfg.Routing.spec(task) }
func (p *HTTPProvider) Close() {
	p.client.CloseIdleConnections()
}

// Redact configuration even when a caller accidentally prints the provider.
func (p *HTTPProvider) String() string   { return "HTTPProvider(redacted)" }
func (p *HTTPProvider) GoString() string { return p.String() }
func (p *HTTPProvider) MarshalJSON() ([]byte, error) {
	return []byte(`{"provider":"http","credentials":"redacted"}`), nil
}
func (p *HTTPProvider) gate() error {
	if !p.cfg.PaidCallsEnabled {
		return &ProviderError{Code: "paid_calls_disabled"}
	}
	if !p.cfg.Routing.TariffsConfirmed || (p.cfg.Routing.PriceUnitTokens != 1000 && p.cfg.Routing.PriceUnitTokens != 1_000_000) {
		return &ProviderError{Code: "tariff_unit_unconfirmed"}
	}
	if !p.cfg.BudgetContractConfirmed || p.cfg.KeyInfoURL == "" || p.cfg.RemainingPath == "" {
		return &ProviderError{Code: "budget_contract_unconfirmed"}
	}
	if !p.cfg.UsageContractConfirmed || !p.cfg.TokenLimitConfirmed || p.cfg.TokenLimitParameter == "" {
		return &ProviderError{Code: "request_contract_unconfirmed"}
	}
	for _, task := range []string{"tag", "profile", "insight", "check", "chat"} {
		spec, e := p.Spec(task)
		if e != nil {
			return e
		}
		price, ok := p.cfg.Routing.Prices[spec.Model]
		if !ok {
			return &ProviderError{Code: "model_price_missing"}
		}
		cost, e := price.Cost(1, 1, p.cfg.Routing.PriceUnitTokens)
		if e != nil || cost <= 0 {
			return &ProviderError{Code: "model_price_missing"}
		}
	}
	return nil
}
func (p *HTTPProvider) payload(c Call) ([]byte, int, error) {
	if ValidateInput(c) != nil {
		return nil, 0, ErrInvalid
	}
	if containsCredential(c.Input, p.cfg.APIKey) {
		return nil, 0, &ProviderError{Code: "credential_in_input"}
	}
	spec, e := p.Spec(c.Task)
	if e != nil {
		return nil, 0, e
	}
	prompt, e := Prompt(c.Task)
	if e != nil {
		return nil, 0, e
	}
	schema, e := OutputSchema(c.Task)
	if e != nil {
		return nil, 0, e
	}
	system := prompt + "\nОтвет — только JSON по схеме:\n" + string(schema)
	// Byte count is a conservative local token bound, including framing margin;
	// it is explicitly an estimate, not vendor usage or an exact tokenizer.
	bound := len(system) + len(c.Input) + 1024
	if bound > spec.MaxInputTokens {
		return nil, 0, &ProviderError{Code: "input_context_limit"}
	}
	body := map[string]any{"model": spec.Model, "messages": []map[string]string{{"role": "system", "content": system}, {"role": "user", "content": string(c.Input)}}}
	if p.cfg.TokenLimitConfirmed {
		body[p.cfg.TokenLimitParameter] = spec.MaxOutputTokens
	}
	b, e := json.Marshal(body)
	return b, bound, e
}
func (p *HTTPProvider) Reserve(c Call) (int64, error) {
	if e := p.gate(); e != nil {
		return 0, e
	}
	_, tokens, e := p.payload(c)
	if e != nil {
		return 0, e
	}
	s, e := p.Spec(c.Task)
	if e != nil {
		return 0, e
	}
	price, ok := p.cfg.Routing.Prices[s.Model]
	if !ok {
		return 0, &ProviderError{Code: "model_price_missing"}
	}
	return price.Cost(tokens, s.MaxOutputTokens, p.cfg.Routing.PriceUnitTokens)
}
func (p *HTTPProvider) Cost(task string, u Usage) (int64, error) {
	if !u.Known || u.InputTokens < 0 || u.OutputTokens < 0 || u.ReasoningTokens < 0 || u.ReasoningTokens > u.OutputTokens {
		return 0, ErrInvalid
	}
	s, e := p.Spec(task)
	if e != nil {
		return 0, e
	}
	return p.cfg.Routing.Prices[s.Model].Cost(u.InputTokens, u.OutputTokens, p.cfg.Routing.PriceUnitTokens)
}
func (p *HTTPProvider) acquire(ctx context.Context) error {
	select {
	case p.slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return &ProviderError{Code: "request_cancelled", Retryable: true}
	}
}
func boundedRead(r io.Reader, max int64) ([]byte, error) {
	b, e := io.ReadAll(io.LimitReader(r, max+1))
	if e != nil || int64(len(b)) > max {
		return nil, ErrInvalid
	}
	return b, nil
}
func retryAfter(s string) time.Duration {
	if n, e := strconv.Atoi(s); e == nil && n >= 0 && n <= 300 {
		return time.Duration(n) * time.Second
	}
	if date, e := http.ParseTime(s); e == nil {
		d := time.Until(date)
		if d > 0 && d <= 5*time.Minute {
			return d
		}
	}
	return 2 * time.Second
}
func (p *HTTPProvider) Complete(ctx context.Context, c Call) (json.RawMessage, Usage, error) {
	if e := p.gate(); e != nil {
		return nil, Usage{}, e
	}
	body, _, e := p.payload(c)
	if e != nil {
		return nil, Usage{}, e
	}
	if e = p.acquire(ctx); e != nil {
		return nil, Usage{}, e
	}
	defer func() { <-p.slots }()
	endpoints := []string{p.cfg.BaseURL}
	if p.cfg.FallbackURL != "" {
		endpoints = append(endpoints, p.cfg.FallbackURL)
	}
	for i, base := range endpoints {
		var sent atomic.Bool
		trace := &httptrace.ClientTrace{WroteHeaders: func() { sent.Store(true) }}
		call := httptrace.WithClientTrace(ctx, trace)
		request, e := http.NewRequestWithContext(call, http.MethodPost, base+"/chat/completions", bytes.NewReader(body))
		if e != nil {
			return nil, Usage{}, &ProviderError{Code: "invalid_endpoint"}
		}
		request.Header.Set("Authorization", "Bearer "+p.cfg.APIKey)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Accept", "application/json")
		response, e := p.client.Do(request)
		if e != nil {
			if response != nil && response.Body != nil {
				_ = response.Body.Close()
			}
			if sent.Load() {
				return nil, Usage{Sent: true}, &ProviderError{Code: "llm_outcome_unknown", Ambiguous: true}
			}
			if ctx.Err() != nil {
				return nil, Usage{}, &ProviderError{Code: "request_cancelled", Retryable: true}
			}
			if i+1 < len(endpoints) {
				continue
			}
			return nil, Usage{}, &ProviderError{Code: "provider_connect_failed", Retryable: true}
		}
		u := Usage{Sent: true}
		b, readErr := boundedRead(response.Body, p.cfg.MaxResponseBytes)
		_ = response.Body.Close()
		if readErr != nil {
			return nil, u, &ProviderError{Code: "llm_outcome_unknown", Ambiguous: true}
		}
		if bytes.Contains(bytes.ToLower(b), []byte("budget has been exceeded")) {
			return nil, u, &ProviderError{Code: "llm_budget_exceeded"}
		}
		if response.StatusCode == http.StatusTooManyRequests {
			return nil, u, &ProviderError{Code: "provider_rate_limited", Retryable: true, Wait: retryAfter(response.Header.Get("Retry-After"))}
		}
		if Contains([]string{"400", "401", "403", "404", "422"}, strconv.Itoa(response.StatusCode)) {
			return nil, u, &ProviderError{Code: "provider_http_" + strconv.Itoa(response.StatusCode)}
		}
		if response.StatusCode != 200 {
			return nil, u, &ProviderError{Code: "llm_outcome_unknown", Ambiguous: true}
		}
		var wire struct {
			Model   string `json:"model"`
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
			Usage *struct {
				Prompt     *int `json:"prompt_tokens"`
				Completion *int `json:"completion_tokens"`
				Details    struct {
					Reasoning int `json:"reasoning_tokens"`
				} `json:"completion_tokens_details"`
			} `json:"usage"`
		}
		// Compatible gateways may add metadata. Only the selected content is parsed
		// against our strict application schema, never reasoning_content/tool calls.
		if json.Unmarshal(b, &wire) != nil || wire.Usage == nil || wire.Usage.Prompt == nil || wire.Usage.Completion == nil || *wire.Usage.Prompt < 0 || *wire.Usage.Completion < 0 || wire.Usage.Details.Reasoning < 0 || wire.Usage.Details.Reasoning > *wire.Usage.Completion {
			return nil, u, &ProviderError{Code: "llm_outcome_unknown", Ambiguous: true}
		}
		u.Known = true
		u.InputTokens = *wire.Usage.Prompt
		u.OutputTokens = *wire.Usage.Completion
		u.ReasoningTokens = wire.Usage.Details.Reasoning
		spec, _ := p.Spec(c.Task)
		if (wire.Model != "" && wire.Model != spec.Model) || len(wire.Choices) != 1 || wire.Choices[0].FinishReason != "stop" || len(wire.Choices[0].Message.Content) == 0 {
			return nil, u, &ProviderError{Code: "invalid_provider_response"}
		}
		content := []byte(wire.Choices[0].Message.Content)
		if containsCredential(content, p.cfg.APIKey) {
			return nil, u, &ProviderError{Code: "credential_in_response"}
		}
		if ValidateOutput(c, content) != nil {
			return nil, u, &ProviderError{Code: "invalid_model_output"}
		}
		return content, u, nil
	}
	return nil, Usage{}, &ProviderError{Code: "provider_connect_failed", Retryable: true}
}

// Inspect decoded values too: a JSON unicode escape must not bypass redaction.
func containsCredential(b []byte, key string) bool {
	if key == "" {
		return false
	}
	if bytes.Contains(b, []byte(key)) {
		return true
	}
	var value any
	if json.Unmarshal(b, &value) != nil {
		return false
	}
	var has func(any) bool
	has = func(v any) bool {
		switch x := v.(type) {
		case string:
			return strings.Contains(x, key)
		case map[string]any:
			for k, v := range x {
				if strings.Contains(k, key) || has(v) {
					return true
				}
			}
		case []any:
			for _, v := range x {
				if has(v) {
					return true
				}
			}
		}
		return false
	}
	return has(value)
}
func (p *HTTPProvider) Budget(ctx context.Context) (BudgetSnapshot, error) {
	if e := p.gate(); e != nil {
		return BudgetSnapshot{}, e
	}
	call, cancel := context.WithTimeout(ctx, min(p.cfg.Timeout, 3*time.Second))
	defer cancel()
	ctx = call
	if e := p.acquire(ctx); e != nil {
		return BudgetSnapshot{}, e
	}
	defer func() { <-p.slots }()
	started := time.Now().UTC()
	urls := []string{p.cfg.KeyInfoURL}
	if p.cfg.FallbackURL != "" {
		u, _ := url.Parse(p.cfg.KeyInfoURL)
		v, _ := url.Parse(p.cfg.FallbackURL)
		if u.Host != v.Host {
			u.Host = v.Host
			urls = append(urls, u.String())
		}
	}
	for _, endpoint := range urls {
		req, e := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if e != nil {
			return BudgetSnapshot{}, ErrInvalid
		}
		req.Header.Set("Authorization", "Bearer "+p.cfg.APIKey)
		req.Header.Set("Accept", "application/json")
		response, e := p.client.Do(req)
		if e != nil {
			if response != nil && response.Body != nil {
				_ = response.Body.Close()
			}
			continue
		}
		b, err := boundedRead(response.Body, p.cfg.MaxResponseBytes)
		_ = response.Body.Close()
		if err != nil {
			return BudgetSnapshot{}, &ProviderError{Code: "budget_contract_mismatch"}
		}
		if bytes.Contains(bytes.ToLower(b), []byte("budget has been exceeded")) {
			return BudgetSnapshot{}, &ProviderError{Code: "llm_budget_exceeded"}
		}
		if Contains([]string{"400", "401", "403", "404", "422"}, strconv.Itoa(response.StatusCode)) {
			return BudgetSnapshot{}, &ProviderError{Code: "provider_http_" + strconv.Itoa(response.StatusCode)}
		}
		if response.StatusCode == 429 {
			return BudgetSnapshot{}, &ProviderError{Code: "provider_rate_limited", Retryable: true, Wait: retryAfter(response.Header.Get("Retry-After"))}
		}
		if response.StatusCode != 200 {
			continue
		}
		decoder := json.NewDecoder(bytes.NewReader(b))
		decoder.UseNumber()
		var data any
		if decoder.Decode(&data) != nil || decoder.Decode(new(any)) != io.EOF {
			return BudgetSnapshot{}, &ProviderError{Code: "budget_contract_mismatch"}
		}
		value, ok := jsonPointer(data, p.cfg.RemainingPath)
		if !ok {
			return BudgetSnapshot{}, &ProviderError{Code: "budget_contract_mismatch"}
		}
		number, ok := value.(json.Number)
		if !ok {
			return BudgetSnapshot{}, &ProviderError{Code: "budget_contract_mismatch"}
		}
		amount, err := RubToNano(number.String())
		if err != nil {
			return BudgetSnapshot{}, &ProviderError{Code: "budget_contract_mismatch"}
		}
		return BudgetSnapshot{RemainingNano: amount, StartedAt: started}, nil
	}
	return BudgetSnapshot{}, &ProviderError{Code: "budget_status_unavailable", Retryable: true}
}
func jsonPointer(v any, path string) (any, bool) {
	if len(path) < 2 || len(path) > 256 || path[0] != '/' {
		return nil, false
	}
	for _, part := range strings.Split(path[1:], "/") {
		part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
		m, ok := v.(map[string]any)
		if !ok {
			return nil, false
		}
		v, ok = m[part]
		if !ok {
			return nil, false
		}
	}
	return v, true
}
