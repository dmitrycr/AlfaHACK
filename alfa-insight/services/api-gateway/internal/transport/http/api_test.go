package transport

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"alfa.local/api-gateway/internal/service"
	"alfa.local/shared/platform"
	"alfa.local/shared/rpc"
	"golang.org/x/crypto/bcrypt"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

type memoryAuth struct {
	mu       sync.Mutex
	users    map[string]service.User
	sessions map[string]service.Session
	attempts map[string]int
}

func (m *memoryAuth) FindUser(_ context.Context, email string) (service.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.users[email]
	if !ok {
		return u, service.ErrCredentials
	}
	return u, nil
}
func (m *memoryAuth) AllowAttempt(_ context.Context, key string, limit int, now time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key += now.Truncate(time.Minute).String()
	m.attempts[key]++
	return m.attempts[key] <= limit, nil
}
func (m *memoryAuth) CreateSession(_ context.Context, s service.Session, old string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, old)
	m.sessions[s.TokenHash] = s
	return nil
}
func (m *memoryAuth) Session(_ context.Context, hash string, now time.Time, idle time.Duration) (service.Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[hash]
	if !ok || !now.Before(s.ExpiresAt) || !s.LastSeen.After(now.Add(-idle)) {
		return s, service.ErrSession
	}
	s.LastSeen = now
	m.sessions[hash] = s
	return s, nil
}
func (m *memoryAuth) DeleteSession(_ context.Context, hash string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, hash)
	return nil
}

type companyMock struct{ key string }

func (m companyMock) GetCompany(ctx context.Context, r *rpc.Request, _ ...grpc.CallOption) (*rpc.Response, error) {
	md, _ := metadata.FromOutgoingContext(ctx)
	values := md.Get("authorization")
	if len(values) != 1 {
		return nil, errors.New("no signed context")
	}
	c, e := platform.Verify(strings.TrimPrefix(values[0], "Bearer "), m.key, "company-service", time.Now())
	if e != nil || c.CompanyID != r.CompanyId {
		return nil, errors.New("invalid signed tenant")
	}
	b, _ := json.Marshal(map[string]string{"id": r.CompanyId})
	return &rpc.Response{Payload: b}, nil
}
func (m companyMock) GetProfile(ctx context.Context, r *rpc.Request, opts ...grpc.CallOption) (*rpc.Response, error) {
	return m.GetCompany(ctx, r, opts...)
}
func (m companyMock) ReplaceProfile(context.Context, *rpc.Request, ...grpc.CallOption) (*rpc.Response, error) {
	return nil, errors.New("not exposed")
}
func setup(t *testing.T) (*http.Client, *httptest.Server, *service.Auth) {
	t.Helper()
	hash, _ := bcrypt.GenerateFromPassword([]byte("DemoOnly-2026!"), 4)
	store := &memoryAuth{users: map[string]service.User{"metal@example.test": {ID: "metal-user", Email: "metal@example.test", CompanyID: "company-metal", PasswordHash: hash}, "build@example.test": {ID: "build-user", Email: "build@example.test", CompanyID: "company-build", PasswordHash: hash}}, sessions: map[string]service.Session{}, attempts: map[string]int{}}
	auth := service.NewAuth(store)
	key := strings.Repeat("k", 32)
	api := &API{Auth: auth, Key: key, Origin: "http://frontend.test", Company: companyMock{key}}
	server := httptest.NewServer(platform.Recover(api.Handler()))
	t.Cleanup(server.Close)
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar}, server, auth
}
func send(t *testing.T, client *http.Client, base, method, path, body, csrf string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(method, base+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if csrf != "" {
		req.Header.Set("X-CSRF-Token", csrf)
	}
	response, e := client.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	return response
}
func login(t *testing.T, client *http.Client, base, email string) string {
	t.Helper()
	r := send(t, client, base, "POST", "/api/v1/auth/login", `{"email":"`+email+`","password":"DemoOnly-2026!"}`, "")
	defer r.Body.Close()
	if r.StatusCode != 200 {
		b, _ := io.ReadAll(r.Body)
		t.Fatalf("login %d %s", r.StatusCode, b)
	}
	for _, c := range r.Cookies() {
		if !c.HttpOnly || c.SameSite != http.SameSiteLaxMode {
			t.Fatal("unsafe cookie")
		}
	}
	var v struct {
		CSRF string `json:"csrf_token"`
	}
	json.NewDecoder(r.Body).Decode(&v)
	return v.CSRF
}
func TestSessionIsolationAndLogout(t *testing.T) {
	client, server, _ := setup(t)
	r := send(t, client, server.URL, "GET", "/api/v1/companies/current", "", "")
	r.Body.Close()
	if r.StatusCode != 401 {
		t.Fatal(r.StatusCode)
	}
	csrf := login(t, client, server.URL, "metal@example.test")
	r = send(t, client, server.URL, "GET", "/api/v1/companies/current", "", "")
	var company map[string]string
	json.NewDecoder(r.Body).Decode(&company)
	r.Body.Close()
	if company["id"] != "company-metal" {
		t.Fatal(company)
	}
	r = send(t, client, server.URL, "GET", "/api/v1/companies/current?company_id=company-build", "", "")
	r.Body.Close()
	if r.StatusCode != 403 {
		t.Fatal("tenant spoof accepted")
	}
	r = send(t, client, server.URL, "POST", "/api/v1/auth/logout", "", "")
	r.Body.Close()
	if r.StatusCode != 403 {
		t.Fatal("logout without CSRF accepted")
	}
	r = send(t, client, server.URL, "POST", "/api/v1/auth/logout", "", csrf)
	r.Body.Close()
	if r.StatusCode != 204 {
		t.Fatal(r.StatusCode)
	}
	r = send(t, client, server.URL, "GET", "/api/v1/auth/me", "", "")
	r.Body.Close()
	if r.StatusCode != 401 {
		t.Fatal("session survived logout")
	}
}
func TestLoginValidationAndOrigin(t *testing.T) {
	client, server, _ := setup(t)
	for _, body := range []string{`{"email":"metal@example.test","password":"wrong"}`, `{"email":"missing@example.test","password":"wrong"}`} {
		r := send(t, client, server.URL, "POST", "/api/v1/auth/login", body, "")
		r.Body.Close()
		if r.StatusCode != 401 {
			t.Fatal(r.StatusCode)
		}
	}
	req, _ := http.NewRequest("POST", server.URL+"/api/v1/auth/login", strings.NewReader(`{}`))
	req.Header.Set("Origin", "https://evil.test")
	req.Header.Set("Content-Type", "application/json")
	r, e := client.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	r.Body.Close()
	if r.StatusCode != 403 {
		t.Fatal("foreign origin accepted")
	}
	r = send(t, client, server.URL, "POST", "/api/v1/auth/login", `{"email":"x","password":"x","company_id":"spoof"}`, "")
	r.Body.Close()
	if r.StatusCode != 400 {
		t.Fatal("unknown field accepted")
	}
}
func TestSessionExpiryAndTampering(t *testing.T) {
	client, server, auth := setup(t)
	login(t, client, server.URL, "metal@example.test")
	auth.Now = func() time.Time { return time.Now().Add(auth.Idle + time.Second) }
	r := send(t, client, server.URL, "GET", "/api/v1/auth/me", "", "")
	r.Body.Close()
	if r.StatusCode != 401 {
		t.Fatal("idle session survived")
	}
	auth.Now = time.Now
	login(t, client, server.URL, "metal@example.test")
	req, _ := http.NewRequest("GET", server.URL+"/api/v1/auth/me", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: strings.Repeat("x", 64)})
	noJar := &http.Client{}
	r, e := noJar.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	r.Body.Close()
	if r.StatusCode != 401 {
		t.Fatal("forged session accepted")
	}
}
func TestRateLimitAndPagination(t *testing.T) {
	client, server, auth := setup(t)
	m := auth.Store.(*memoryAuth)
	now := auth.Now()
	m.attempts[platform.Hash("account:metal@example.test")+now.Truncate(time.Minute).String()] = 10
	r := send(t, client, server.URL, "POST", "/api/v1/auth/login", `{"email":"metal@example.test","password":"DemoOnly-2026!"}`, "")
	r.Body.Close()
	if r.StatusCode != 429 {
		t.Fatal("rate limit not enforced")
	}
	login(t, client, server.URL, "build@example.test")
	r = send(t, client, server.URL, "GET", "/api/v1/companies/current?limit=10000000000", "", "")
	r.Body.Close()
	if r.StatusCode != 400 {
		t.Fatal("unbounded page accepted")
	}
}
