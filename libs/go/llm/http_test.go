package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/url"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type localTransport struct {
	rtFunc
	pool *http.Transport
}

func (t localTransport) CloseIdleConnections() { t.pool.CloseIdleConnections() }
func httpFixture() Call {
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	return Call{Task: "tag", Scope: "public", Input: canonical(TagInput{EventID: "event-http", InputRevision: 1, Title: "Изменение ставки", Text: "Тестовая новость о ставке и финансировании.", Sources: []Source{{URL: "https://news.example.test/rate", Name: "Тестовый источник", PublishedAt: now, Kind: "news"}}, CollectedAt: now})}
}
func httpConfig() HTTPConfig {
	route := DefaultRouting()
	route.TariffsConfirmed = true
	route.PriceUnitTokens = 1_000_000
	return HTTPConfig{BaseURL: PrimaryURL, FallbackURL: FallbackURL, APIKey: "test-only-secret-do-not-log", KeyInfoURL: "https://changellenge.up.railway.app/key/info", RemainingPath: "/remaining_rub", BudgetContractConfirmed: true, UsageContractConfirmed: true, TokenLimitConfirmed: true, TokenLimitParameter: "max_tokens", PaidCallsEnabled: true, Routing: route, Timeout: time.Second, MaxParallel: 4, MaxResponseBytes: 1 << 20}
}
func localHTTP(t *testing.T, cfg HTTPConfig, handler http.HandlerFunc) (*HTTPProvider, *atomic.Int32) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	target, _ := url.Parse(server.URL)
	transport := &http.Transport{Proxy: nil, MaxConnsPerHost: 4, MaxIdleConnsPerHost: 4, MaxIdleConns: 4}
	t.Cleanup(transport.CloseIdleConnections)
	count := &atomic.Int32{}
	rt := rtFunc(func(r *http.Request) (*http.Response, error) {
		count.Add(1)
		cloned := r.Clone(r.Context())
		u := *r.URL
		u.Scheme = target.Scheme
		u.Host = target.Host
		cloned.URL = &u
		return transport.RoundTrip(cloned)
	})
	p, e := newHTTPProvider(cfg, localTransport{rtFunc: rt, pool: transport})
	if e != nil {
		t.Fatal(e)
	}
	return p, count
}
func responseFor(c Call) map[string]any {
	body, _, _ := (Mock{}).Complete(context.Background(), c)
	return map[string]any{"model": GLM, "choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": string(body)}, "finish_reason": "stop"}}, "usage": map[string]any{"prompt_tokens": 100, "completion_tokens": 100, "completion_tokens_details": map[string]any{"reasoning_tokens": 20}}}
}
func TestHTTPExactRequestAndBudgetContract(t *testing.T) {
	cfg := httpConfig()
	input := httpFixture()
	posts := 0
	p, _ := localHTTP(t, cfg, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+cfg.APIKey {
			t.Error("wrong auth header")
		}
		if r.URL.Path == "/key/info" {
			if r.Method != "GET" {
				t.Error("wrong budget method")
			}
			_, _ = io.WriteString(w, `{"remaining_rub":499.123456789}`)
			return
		}
		posts++
		if r.Method != "POST" || r.URL.Path != "/v1/chat/completions" {
			t.Error("wrong completion endpoint")
		}
		b, _ := io.ReadAll(r.Body)
		if strings.Contains(string(b), cfg.APIKey) {
			t.Error("secret included in prompt")
		}
		var body map[string]json.RawMessage
		if json.Unmarshal(b, &body) != nil || len(body) != 3 {
			t.Error("unexpected wire fields")
		}
		var model string
		_ = json.Unmarshal(body["model"], &model)
		if model != GLM {
			t.Error(model)
		}
		if _, ok := body["max_tokens"]; !ok {
			t.Error("missing confirmed token bound")
		}
		var messages []map[string]any
		_ = json.Unmarshal(body["messages"], &messages)
		if len(messages) != 2 || messages[0]["role"] != "system" || messages[1]["role"] != "user" {
			t.Error("wrong messages")
		}
		_ = json.NewEncoder(w).Encode(responseFor(input))
	})
	budget, e := p.Budget(context.Background())
	if e != nil || budget.RemainingNano != 499_123_456_789 {
		t.Fatal(budget, e)
	}
	output, u, e := p.Complete(context.Background(), input)
	if e != nil || !u.Known || u.ReasoningTokens != 20 || ValidateOutput(input, output) != nil || posts != 1 {
		t.Fatal(u, e)
	}
	cost, e := p.Cost("tag", u)
	if e != nil || cost != 2_210_000 {
		t.Fatal("reasoning was double counted", cost, e)
	}
	text := fmt.Sprintf("%v %+v %#v %v", p, p, cfg, cfg)
	if strings.Contains(text, cfg.APIKey) {
		t.Fatal("secret in debug formatting")
	}
}
func TestHTTPGatesNoNetworkAndEndpointAllowlist(t *testing.T) {
	for _, mutate := range []func(*HTTPConfig){func(c *HTTPConfig) { c.PaidCallsEnabled = false }, func(c *HTTPConfig) { c.Routing.TariffsConfirmed = false }, func(c *HTTPConfig) { c.Routing.PriceUnitTokens = 0 }, func(c *HTTPConfig) { c.BudgetContractConfirmed = false }, func(c *HTTPConfig) { c.TokenLimitConfirmed = false }, func(c *HTTPConfig) { c.UsageContractConfirmed = false }} {
		cfg := httpConfig()
		mutate(&cfg)
		var requests atomic.Int32
		p, e := newHTTPProvider(cfg, rtFunc(func(*http.Request) (*http.Response, error) {
			requests.Add(1)
			return nil, errors.New("should not connect")
		}))
		if e != nil {
			t.Fatal(e)
		}
		if _, _, e = p.Complete(context.Background(), httpFixture()); e == nil {
			t.Fatal("gate passed")
		}
		if requests.Load() != 0 {
			t.Fatal("network before confirmation")
		}
	}
	for _, base := range []string{"http://changellenge.up.railway.app/v1", "https://evil.example/v1", "https://changellenge.up.railway.app/v1?token=secret", "https://user:secret@changellenge.up.railway.app/v1"} {
		cfg := httpConfig()
		cfg.BaseURL = base
		if _, e := NewHTTPProvider(cfg); e == nil {
			t.Fatal("unsafe endpoint accepted")
		}
	}
	cfg := httpConfig()
	cfg.Routing.Agents["tag"] = AgentSpec{Model: "qwen/qwen3-vl-30b-a3b-instruct", MaxInputTokens: 1000, MaxOutputTokens: 1000}
	if _, e := NewHTTPProvider(cfg); e == nil {
		t.Fatal("vision model accepted")
	}
}
func TestHTTPErrorsNeverExposeSecret(t *testing.T) {
	for _, code := range []int{400, 401, 403, 404, 422, 429, 500, 502, 302} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			cfg := httpConfig()
			p, count := localHTTP(t, cfg, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Retry-After", "7")
				w.Header().Set("Location", "https://evil.example/steal")
				w.WriteHeader(code)
				_, _ = io.WriteString(w, `{"error":{"message":"`+cfg.APIKey+`"}}`)
			})
			_, _, e := p.Complete(context.Background(), httpFixture())
			var pe *ProviderError
			if !errors.As(e, &pe) {
				t.Fatal(e)
			}
			if strings.Contains(fmt.Sprintf("%+v", e), cfg.APIKey) || count.Load() != 1 {
				t.Fatal("leak, redirect or unsafe fallback")
			}
			if code == 429 && (!pe.Retryable || pe.Wait != 7*time.Second) {
				t.Fatal(pe)
			}
			if code >= 500 && !pe.Ambiguous {
				t.Fatal("5xx not classified unknown")
			}
		})
	}
}
func TestHTTPNoReplayAfterHeadersAndSafeConnectFailover(t *testing.T) {
	cfg := httpConfig()
	var attempts atomic.Int32
	p, e := newHTTPProvider(cfg, rtFunc(func(r *http.Request) (*http.Response, error) {
		attempts.Add(1)
		if r.URL.Host == "changellenge.up.railway.app" {
			return nil, errors.New("connection refused")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(canonical(responseFor(httpFixture()))))), Header: make(http.Header), Request: r}, nil
	}))
	if e != nil {
		t.Fatal(e)
	}
	if _, _, e = p.Complete(context.Background(), httpFixture()); e != nil || attempts.Load() != 2 {
		t.Fatal(attempts.Load(), e)
	}
	attempts.Store(0)
	p, e = newHTTPProvider(cfg, rtFunc(func(r *http.Request) (*http.Response, error) {
		attempts.Add(1)
		if trace := httptrace.ContextClientTrace(r.Context()); trace != nil && trace.WroteHeaders != nil {
			trace.WroteHeaders()
		}
		return nil, errors.New("connection lost after write")
	}))
	if e != nil {
		t.Fatal(e)
	}
	_, _, e = p.Complete(context.Background(), httpFixture())
	var pe *ProviderError
	if !errors.As(e, &pe) || !pe.Ambiguous || attempts.Load() != 1 {
		t.Fatal(attempts.Load(), e)
	}
}
func TestHTTPMalformedAndUncertainUsage(t *testing.T) {
	input := httpFixture()
	for _, scenario := range []string{"missing_usage", "truncated_json", "oversize", "secret_content", "reasoning_invalid", "length"} {
		t.Run(scenario, func(t *testing.T) {
			cfg := httpConfig()
			p, _ := localHTTP(t, cfg, func(w http.ResponseWriter, r *http.Request) {
				body := responseFor(input)
				switch scenario {
				case "missing_usage":
					delete(body, "usage")
				case "truncated_json":
					_, _ = io.WriteString(w, `{"choices":`)
					return
				case "oversize":
					_, _ = io.WriteString(w, strings.Repeat("x", int(cfg.MaxResponseBytes)+1))
					return
				case "secret_content":
					body["choices"] = []any{map[string]any{"message": map[string]any{"content": cfg.APIKey}, "finish_reason": "stop"}}
				case "reasoning_invalid":
					body["usage"] = map[string]any{"prompt_tokens": 10, "completion_tokens": 10, "completion_tokens_details": map[string]any{"reasoning_tokens": 20}}
				case "length":
					body["choices"] = []any{map[string]any{"message": map[string]any{"content": "{}"}, "finish_reason": "length"}}
				}
				_ = json.NewEncoder(w).Encode(body)
			})
			_, _, e := p.Complete(context.Background(), input)
			if e == nil || strings.Contains(e.Error(), cfg.APIKey) {
				t.Fatal("invalid result accepted")
			}
		})
	}
}

type observedBody struct {
	io.ReadCloser
	closed *atomic.Int32
}

func (b *observedBody) Close() error { b.closed.Add(1); return b.ReadCloser.Close() }
func TestHTTPResourcesStayBounded(t *testing.T) {
	cfg := httpConfig()
	var sent, closed atomic.Int32
	body := string(canonical(responseFor(httpFixture())))
	p, e := newHTTPProvider(cfg, rtFunc(func(r *http.Request) (*http.Response, error) {
		sent.Add(1)
		status := 200
		content := body
		if sent.Load()%3 == 0 {
			status = 400
			content = `{"error":"rejected"}`
		}
		return &http.Response{StatusCode: status, Body: &observedBody{io.NopCloser(strings.NewReader(content)), &closed}, Header: make(http.Header), Request: r}, nil
	}))
	if e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 50; i++ {
		_, _, _ = p.Complete(context.Background(), httpFixture())
	}
	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	goroutines := runtime.NumGoroutine()
	for i := 0; i < 1000; i++ {
		_, _, _ = p.Complete(context.Background(), httpFixture())
	}
	runtime.GC()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	if sent.Load() != closed.Load() {
		t.Fatal("response bodies not closed", sent.Load(), closed.Load())
	}
	if after.HeapAlloc > before.HeapAlloc+8<<20 {
		t.Fatal("unbounded retained heap", before.HeapAlloc, after.HeapAlloc)
	}
	if runtime.NumGoroutine() > goroutines+6 {
		t.Fatal("goroutine growth")
	}
	t.Logf("1000 requests: retained heap before=%d after=%d; goroutines before=%d after=%d; closed=%d/%d", before.HeapAlloc, after.HeapAlloc, goroutines, runtime.NumGoroutine(), closed.Load(), sent.Load())
}
func TestMoneyAndConfiguration(t *testing.T) {
	for unit, want := range map[int64]int64{1_000_000: 28_900_000, 1000: 28_900_000_000} {
		got, e := (Price{"5.10", "17.00"}).Cost(3000, 800, unit)
		if e != nil || got != want {
			t.Fatal(got, e)
		}
	}
	if _, e := (Price{"5.10", "17.00"}).Cost(1, 1, 0); e == nil {
		t.Fatal("unknown denominator accepted")
	}
	c, e := LoadConfig(func(string) string { return "" })
	if e != nil || c.Mode != "mock" || c.Routing.PriceUnitTokens != 0 || c.HTTP.PaidCallsEnabled {
		t.Fatal(c, e)
	}
	_, e = LoadConfig(func(k string) string {
		if k == "LLM_PRICE_UNIT_TOKENS" {
			return "123"
		}
		return ""
	})
	if e == nil {
		t.Fatal("invalid tariff unit accepted")
	}
}

func TestCredentialEscapesAndPreflight(t *testing.T) {
	cfg := httpConfig()
	escaped := strings.ReplaceAll(cfg.APIKey, "-", `\u002d`)
	if !containsCredential([]byte(`{"value":"`+escaped+`"}`), cfg.APIKey) {
		t.Fatal("unicode escaped key bypassed scanner")
	}
	input := httpFixture()
	var tag TagInput
	_ = json.Unmarshal(input.Input, &tag)
	tag.Text = cfg.APIKey
	input.Input = canonical(tag)
	var requests atomic.Int32
	p, e := newHTTPProvider(cfg, rtFunc(func(*http.Request) (*http.Response, error) {
		requests.Add(1)
		return nil, errors.New("should not connect")
	}))
	if e != nil {
		t.Fatal(e)
	}
	if _, _, e = p.Complete(context.Background(), input); e == nil || requests.Load() != 0 {
		t.Fatal("secret was sent in input")
	}
	config, e := LoadConfig(func(string) string { return "" })
	if e != nil {
		t.Fatal(e)
	}
	report := config.Preflight()
	if report.PaymentGateOpen || report.KeyConfigured {
		t.Fatal(report)
	}
}

func TestBudgetAuthAndContractErrorsDoNotRetry(t *testing.T) {
	for _, scenario := range []string{"401", "wrong_shape", "negative"} {
		t.Run(scenario, func(t *testing.T) {
			cfg := httpConfig()
			p, count := localHTTP(t, cfg, func(w http.ResponseWriter, r *http.Request) {
				switch scenario {
				case "401":
					w.WriteHeader(401)
					_, _ = io.WriteString(w, `{"error":"`+cfg.APIKey+`"}`)
				case "wrong_shape":
					_, _ = io.WriteString(w, `{"other":500}`)
				case "negative":
					_, _ = io.WriteString(w, `{"remaining_rub":-1}`)
				}
			})
			_, err := p.Budget(context.Background())
			var pe *ProviderError
			if !errors.As(err, &pe) || !pe.Terminal() || pe.Retryable || count.Load() != 1 || strings.Contains(err.Error(), cfg.APIKey) {
				t.Fatal(count.Load(), err)
			}
		})
	}
}

func TestHTTPRealConnectionsAndCancellation(t *testing.T) {
	cfg := httpConfig()
	var requests, active, peak atomic.Int32
	input := httpFixture()
	p, _ := localHTTP(t, cfg, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_ = r.Body.Close()
		n := requests.Add(1)
		now := active.Add(1)
		defer active.Add(-1)
		for old := peak.Load(); now > old && !peak.CompareAndSwap(old, now); old = peak.Load() {
		}
		if n%20 == 0 {
			select {
			case <-r.Context().Done():
			case <-time.After(150 * time.Millisecond):
			}
			return
		}
		_ = json.NewEncoder(w).Encode(responseFor(input))
	})
	request := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		defer cancel()
		_, _, _ = p.Complete(ctx, input)
	}
	for i := 0; i < 25; i++ {
		request()
	}
	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	g := runtime.NumGoroutine()
	for i := 0; i < 300; i++ {
		request()
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				request()
			}
		}()
	}
	wg.Wait()
	// Cancelled server handlers observe request cancellation before sampling.
	deadline := time.Now().Add(time.Second)
	for active.Load() > 0 && time.Now().Before(deadline) {
		runtime.Gosched()
	}
	runtime.GC()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	if active.Load() != 0 || peak.Load() > int32(cfg.MaxParallel+8) {
		t.Fatal("unbounded active requests", active.Load(), peak.Load())
	}
	if after.HeapAlloc > before.HeapAlloc+(8<<20) || runtime.NumGoroutine() > g+12 {
		t.Fatal("retained resource growth", before.HeapAlloc, after.HeapAlloc, g, runtime.NumGoroutine())
	}
	t.Logf("real local transport: requests=%d peak=%d retained_heap_before=%d after=%d goroutines_before=%d after=%d", requests.Load(), peak.Load(), before.HeapAlloc, after.HeapAlloc, g, runtime.NumGoroutine())
}
