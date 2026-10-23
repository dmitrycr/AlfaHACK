package postgres

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
	transport "alfa.local/api-gateway/internal/transport/http"
	"alfa.local/api-gateway/migrations"
	"alfa.local/shared/model"
	"alfa.local/shared/platform"
	"alfa.local/shared/rpc"
	"alfa.local/shared/testdb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type registrationCompanies struct{ rpc.CompanyServiceClient }

type failedCompanies struct{}

func (failedCompanies) Check(context.Context, model.CompanyCheckInput, bool) (model.CompanyCheck, error) {
	return model.CompanyCheck{}, status.Error(codes.Unavailable, "registry_unavailable")
}

func (registrationCompanies) Check(_ context.Context, in model.CompanyCheckInput, _ bool) (model.CompanyCheck, error) {
	return model.CompanyCheck{CompanyID: "company-inn-9900000017", INN: in.INN, CanonicalName: "ООО «Тест Металл Альфа»", Status: "matched_mock", Provider: "mock-registry-v1", CheckedAt: time.Now()}, nil
}
func (registrationCompanies) GetCompany(context.Context, *rpc.Request, ...grpc.CallOption) (*rpc.Response, error) {
	return &rpc.Response{Payload: []byte(`{"id":"company-inn-9900000017"}`)}, nil
}
func newRegistration(t *testing.T) (Store, *service.Registration) {
	p := testdb.Open(t, "auth")
	ctx := context.Background()
	if e := migrations.Apply(ctx, p); e != nil {
		t.Fatal(e)
	}
	s := Store{p}
	return s, &service.Registration{Store: s, Companies: registrationCompanies{}, Key: strings.Repeat("k", 32), Now: time.Now}
}
func regInput(email string) service.RegistrationInput {
	return service.RegistrationInput{FullName: "Иван Иванов", JobTitle: "Финансовый директор", Email: email, Password: "Registration-Test-2026!", CompanyName: "Тест Металл Альфа", INN: "9900000017"}
}

func TestRegistrationHTTPAccessLifecycle(t *testing.T) {
	store, reg := newRegistration(t)
	ctx := context.Background()
	api := &transport.API{Auth: service.NewAuth(store), Registration: reg, Company: registrationCompanies{}, Key: reg.Key, Origin: "http://frontend.test"}
	server := httptest.NewServer(platform.Recover(api.Handler()))
	defer server.Close()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	request := func(path string, body any, key string, want int) map[string]any {
		t.Helper()
		b, _ := json.Marshal(body)
		req, _ := http.NewRequest("POST", server.URL+path, strings.NewReader(string(b)))
		req.Header.Set("Content-Type", "application/json")
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		res, e := client.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s: %d %s", path, res.StatusCode, raw)
		}
		var v map[string]any
		json.Unmarshal(raw, &v)
		return v
	}
	input := regInput("employee@example.test")
	response := request("/api/v1/auth/register", input, "registration-001", 201)
	user := response["user"].(map[string]any)
	id := user["id"].(string)
	if user["access_status"] != "pending_email" || user["email_verified"] != false || response["email_delivery"] != "mock" {
		t.Fatal(response)
	}
	if _, ok := response["token"]; ok {
		t.Fatal("email proof returned to registering client")
	}
	request("/api/v1/auth/register", input, "registration-001", 200)
	if e := store.DecideMembership(ctx, id, "test-operator", true); !errors.Is(e, service.ErrMembershipState) {
		t.Fatal("approved unverified email", e)
	}
	request("/api/v1/auth/login", map[string]string{"email": input.Email, "password": input.Password}, "", 200)
	business := func(want int) {
		t.Helper()
		res, e := client.Get(server.URL + "/api/v1/companies/current")
		if e != nil {
			t.Fatal(e)
		}
		defer res.Body.Close()
		if res.StatusCode != want {
			raw, _ := io.ReadAll(res.Body)
			t.Fatalf("business %d %s", res.StatusCode, raw)
		}
	}
	business(403)
	token, e := store.MockVerificationToken(ctx, input.Email)
	if e != nil {
		t.Fatal(e)
	}
	request("/api/v1/auth/verify-email", map[string]string{"token": token}, "", 200)
	business(403)
	if e = store.DecideMembership(ctx, id, "test-operator", true); e != nil {
		t.Fatal(e)
	}
	business(200)
	if e = store.DecideMembership(ctx, id, "test-operator", false); e != nil {
		t.Fatal(e)
	}
	business(401)
	request("/api/v1/auth/login", map[string]string{"email": input.Email, "password": input.Password}, "", 403)
	var decisions int
	if e = store.Pool.QueryRow(ctx, `SELECT count(*) FROM auth.membership_decisions WHERE user_id=$1`, id).Scan(&decisions); e != nil || decisions != 2 {
		t.Fatal("membership audit missing", e)
	}
}

func TestRegistrationIdempotencyConcurrencyAndVerification(t *testing.T) {
	store, reg := newRegistration(t)
	ctx := context.Background()
	input := regInput("repeat@example.test")
	var wg sync.WaitGroup
	ids := make(chan string, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, e := reg.Register(ctx, input, "same-request-key")
			if e != nil {
				t.Error(e)
				return
			}
			ids <- v.User.ID
		}()
	}
	wg.Wait()
	close(ids)
	id := ""
	for v := range ids {
		if id != "" && id != v {
			t.Fatal("duplicate users")
		}
		id = v
	}
	changed := input
	changed.JobTitle = "Главный бухгалтер"
	if _, e := reg.Register(ctx, changed, "same-request-key"); !errors.Is(e, service.ErrRegistrationConflict) {
		t.Fatal(e)
	}
	if _, e := reg.Register(ctx, input, "different-request-key"); !errors.Is(e, service.ErrEmailExists) {
		t.Fatal(e)
	}
	reg.Companies = failedCompanies{}
	if v, e := reg.Register(ctx, input, "same-request-key"); e != nil || v.User.ID != id {
		t.Fatal("lost-response recovery requires registry availability", e)
	}
	if _, e := reg.Register(ctx, regInput("unavailable@example.test"), "unavailable-request"); status.Code(e) != codes.Unavailable {
		t.Fatal(e)
	}
	if _, e := store.FindUser(ctx, "unavailable@example.test"); !errors.Is(e, service.ErrCredentials) {
		t.Fatal("registry failure created account", e)
	}
	reg.Companies = registrationCompanies{}
	old, e := store.MockVerificationToken(ctx, input.Email)
	if e != nil {
		t.Fatal(e)
	}
	if e = reg.Resend(ctx, input.Email); e != nil {
		t.Fatal(e)
	}
	if _, e = reg.Verify(ctx, old); !errors.Is(e, service.ErrVerification) {
		t.Fatal("old token still valid", e)
	}
	token, e := store.MockVerificationToken(ctx, input.Email)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = reg.Verify(ctx, token); e != nil {
		t.Fatal(e)
	}
	if _, e = reg.Verify(ctx, token); !errors.Is(e, service.ErrVerification) {
		t.Fatal("token reused", e)
	}
	if e = reg.Resend(ctx, "unknown@example.test"); e != nil {
		t.Fatal("unknown email should receive same acknowledgement", e)
	}
	other, e := reg.Register(ctx, regInput("expired@example.test"), "expiry-request-key")
	if e != nil {
		t.Fatal(e)
	}
	token, e = store.MockVerificationToken(ctx, other.User.Email)
	if e != nil {
		t.Fatal(e)
	}
	reg.Now = func() time.Time { return time.Now().Add(31 * time.Minute) }
	if _, e = reg.Verify(ctx, token); !errors.Is(e, service.ErrVerification) {
		t.Fatal("expired token accepted", e)
	}
}
