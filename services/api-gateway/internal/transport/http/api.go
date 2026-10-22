package transport

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"mime"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"alfa.local/api-gateway/internal/service"
	"alfa.local/contracts"
	"alfa.local/shared/platform"
	"alfa.local/shared/rpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type API struct {
	Auth      *service.Auth
	Company   rpc.CompanyServiceClient
	News      rpc.NewsServiceClient
	Events    rpc.EventServiceClient
	Analytics rpc.AnalyticsServiceClient
	Key       string
	Origin    string
	Secure    bool
}

const sessionCookie = "alfa_session"
const csrfCookie = "alfa_csrf"

func (a *API) Handler() *http.ServeMux {
	m := http.NewServeMux()
	m.HandleFunc("GET /openapi.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(contracts.OpenAPI)
	})
	m.HandleFunc("POST /api/v1/auth/login", a.login)
	m.HandleFunc("POST /api/v1/auth/logout", a.protect(a.logout))
	m.HandleFunc("GET /api/v1/auth/me", a.protect(a.me))
	m.HandleFunc("GET /api/v1/companies/current", a.protect(a.proxy("company-service", func(ctx context.Context, r *rpc.Request) (*rpc.Response, error) { return a.Company.GetCompany(ctx, r) })))
	m.HandleFunc("GET /api/v1/companies/current/profile", a.protect(a.proxy("company-service", func(ctx context.Context, r *rpc.Request) (*rpc.Response, error) { return a.Company.GetProfile(ctx, r) })))
	m.HandleFunc("GET /api/v1/news", a.protect(a.proxy("news-service", func(ctx context.Context, r *rpc.Request) (*rpc.Response, error) { return a.News.ListArticles(ctx, r) })))
	m.HandleFunc("GET /api/v1/news/{id}", a.protect(a.proxy("news-service", func(ctx context.Context, r *rpc.Request) (*rpc.Response, error) { return a.News.GetArticle(ctx, r) })))
	m.HandleFunc("POST /api/v1/news/submissions", a.protect(a.submit))
	m.HandleFunc("GET /api/v1/news/submissions/{id}", a.protect(a.proxy("news-service", func(ctx context.Context, r *rpc.Request) (*rpc.Response, error) { return a.News.GetSubmission(ctx, r) })))
	m.HandleFunc("GET /api/v1/events", a.protect(a.proxy("event-service", func(ctx context.Context, r *rpc.Request) (*rpc.Response, error) { return a.Events.ListEvents(ctx, r) })))
	m.HandleFunc("GET /api/v1/events/{id}", a.protect(a.proxy("event-service", func(ctx context.Context, r *rpc.Request) (*rpc.Response, error) { return a.Events.GetEvent(ctx, r) })))
	m.HandleFunc("GET /api/v1/analytics/summary", a.protect(a.proxy("analytics-service", func(ctx context.Context, r *rpc.Request) (*rpc.Response, error) {
		return a.Analytics.GetSummary(ctx, r)
	})))
	m.HandleFunc("OPTIONS /api/v1/{path...}", func(w http.ResponseWriter, r *http.Request) {
		if !a.origin(w, r) {
			return
		}
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-CSRF-Token, Idempotency-Key")
		w.WriteHeader(204)
	})
	return m
}
func (a *API) origin(w http.ResponseWriter, r *http.Request) bool {
	if origin := r.Header.Get("Origin"); origin != "" {
		if origin != a.Origin {
			fail(w, 403, "origin_denied")
			return false
		}
		w.Header().Set("Access-Control-Allow-Origin", a.Origin)
		w.Header().Set("Access-Control-Allow-Credentials", "true")
		w.Header().Add("Vary", "Origin")
	}
	return true
}
func fail(w http.ResponseWriter, code int, reason string) {
	platform.JSON(w, code, map[string]string{"code": reason, "request_id": w.Header().Get("X-Request-ID")})
}
func (a *API) login(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !a.origin(w, r) {
		return
	}
	media, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if media != "application/json" {
		fail(w, 415, "json_required")
		return
	}
	var input struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if e := platform.Decode(w, r, &input); e != nil {
		fail(w, 400, "invalid_input")
		return
	}
	ip, _, e := net.SplitHostPort(r.RemoteAddr)
	if e != nil {
		ip = r.RemoteAddr
	}
	old := ""
	if cookie, e := r.Cookie(sessionCookie); e == nil {
		old = cookie.Value
	}
	s, token, csrf, e := a.Auth.Login(r.Context(), input.Email, input.Password, ip, old)
	if e != nil {
		switch {
		case errors.Is(e, service.ErrCredentials):
			fail(w, 401, "invalid_credentials")
		case errors.Is(e, service.ErrRateLimited):
			w.Header().Set("Retry-After", "60")
			fail(w, 429, "too_many_attempts")
		default:
			fail(w, 503, "authentication_unavailable")
		}
		return
	}
	for _, v := range []struct{ name, value string }{{sessionCookie, token}, {csrfCookie, csrf}} {
		http.SetCookie(w, &http.Cookie{Name: v.name, Value: v.value, Path: "/", HttpOnly: true, Secure: a.Secure, SameSite: http.SameSiteLaxMode, Expires: s.ExpiresAt, MaxAge: int(a.Auth.TTL.Seconds())})
	}
	platform.JSON(w, 200, map[string]any{"user": s.User, "csrf_token": csrf, "expires_at": s.ExpiresAt})
}

type protected func(http.ResponseWriter, *http.Request, service.Session)

func (a *API) protect(next protected) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if !a.origin(w, r) {
			return
		}
		cookie, e := r.Cookie(sessionCookie)
		if e != nil {
			fail(w, 401, "authentication_required")
			return
		}
		s, e := a.Auth.Authenticate(r.Context(), cookie.Value)
		if e != nil {
			if errors.Is(e, service.ErrUnavailable) {
				fail(w, 503, "authentication_unavailable")
				return
			}
			fail(w, 401, "invalid_session")
			return
		}
		if company := r.URL.Query().Get("company_id"); company != "" && company != s.User.CompanyID {
			fail(w, 403, "company_access_denied")
			return
		}
		if r.Method != "GET" && r.Method != "HEAD" {
			csrf := r.Header.Get("X-CSRF-Token")
			if len(csrf) != 64 || subtle.ConstantTimeCompare([]byte(platform.Hash(csrf)), []byte(s.CSRFHash)) != 1 {
				fail(w, 403, "csrf_required")
				return
			}
			media, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if r.Method != "DELETE" && r.ContentLength != 0 && media != "application/json" {
				fail(w, 415, "json_required")
				return
			}
		}
		next(w, r, s)
	}
}
func (a *API) me(w http.ResponseWriter, r *http.Request, s service.Session) {
	csrf := ""
	if c, e := r.Cookie(csrfCookie); e == nil && subtle.ConstantTimeCompare([]byte(platform.Hash(c.Value)), []byte(s.CSRFHash)) == 1 {
		csrf = c.Value
	}
	platform.JSON(w, 200, map[string]any{"user": s.User, "csrf_token": csrf, "expires_at": s.ExpiresAt})
}
func (a *API) logout(w http.ResponseWriter, r *http.Request, s service.Session) {
	if e := a.Auth.Store.DeleteSession(r.Context(), s.TokenHash); e != nil {
		fail(w, 503, "authentication_unavailable")
		return
	}
	for _, name := range []string{sessionCookie, csrfCookie} {
		http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: "/", HttpOnly: true, Secure: a.Secure, SameSite: http.SameSiteLaxMode, MaxAge: -1})
	}
	w.WriteHeader(204)
}
func (a *API) rpcContext(r *http.Request, s service.Session, target string) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	correlation := platform.RequestID(r.Context())
	return platform.Outgoing(ctx, platform.Claims{Subject: s.User.ID, CompanyID: s.User.CompanyID, Audience: target, CorrelationID: correlation}, a.Key), cancel
}

type call func(context.Context, *rpc.Request) (*rpc.Response, error)

func (a *API) proxy(target string, fn call) protected {
	return func(w http.ResponseWriter, r *http.Request, s service.Session) {
		limit, offset := 20, 0
		var e error
		if value := r.URL.Query().Get("limit"); value != "" {
			limit, e = strconv.Atoi(value)
			if e != nil {
				fail(w, 400, "invalid_pagination")
				return
			}
		}
		if value := r.URL.Query().Get("offset"); value != "" {
			offset, e = strconv.Atoi(value)
			if e != nil {
				fail(w, 400, "invalid_pagination")
				return
			}
		}
		if _, _, e = platform.Page(limit, offset); e != nil {
			fail(w, 400, "invalid_pagination")
			return
		}
		ctx, cancel := a.rpcContext(r, s, target)
		defer cancel()
		v, e := fn(ctx, &rpc.Request{Id: r.PathValue("id"), CompanyId: s.User.CompanyID, Limit: int32(limit), Offset: int32(offset)})
		writeRPC(w, 200, v, e)
	}
}
func (a *API) submit(w http.ResponseWriter, r *http.Request, s service.Session) {
	var input struct {
		Title       string `json:"title"`
		Text        string `json:"text"`
		URL         string `json:"url"`
		PublishedAt string `json:"published_at,omitempty"`
	}
	if e := platform.Decode(w, r, &input); e != nil {
		fail(w, 400, "invalid_input")
		return
	}
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if len(key) < 8 || len(key) > 128 {
		fail(w, 400, "invalid_idempotency_key")
		return
	}
	b, _ := json.Marshal(map[string]string{"title": input.Title, "text": input.Text, "url": input.URL, "published_at": input.PublishedAt, "idempotency_key": key})
	ctx, cancel := a.rpcContext(r, s, "news-service")
	defer cancel()
	v, e := a.News.Submit(ctx, &rpc.Request{CompanyId: s.User.CompanyID, Payload: b})
	writeRPC(w, 202, v, e)
}
func writeRPC(w http.ResponseWriter, code int, v *rpc.Response, e error) {
	if e != nil {
		switch status.Code(e) {
		case codes.Unauthenticated:
			fail(w, 401, "authentication_required")
		case codes.PermissionDenied:
			fail(w, 403, "company_access_denied")
		case codes.NotFound:
			fail(w, 404, "not_found")
		case codes.InvalidArgument:
			fail(w, 400, status.Convert(e).Message())
		case codes.AlreadyExists, codes.Aborted:
			fail(w, 409, "conflict")
		case codes.DeadlineExceeded:
			fail(w, 504, "upstream_timeout")
		default:
			fail(w, 503, "upstream_unavailable")
		}
		return
	}
	if v == nil || !json.Valid(v.Payload) {
		fail(w, 502, "invalid_upstream_response")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = w.Write(v.Payload)
}
