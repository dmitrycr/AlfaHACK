package platform

import (
	"context"
	"encoding/json"
	"google.golang.org/grpc/metadata"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSignedContext(t *testing.T) {
	key := strings.Repeat("k", 32)
	now := time.Now()
	claims := Claims{Subject: "user", CompanyID: "a", Audience: "news-service", Expires: now.Add(time.Minute).Unix()}
	token := Sign(claims, key)
	for _, tc := range []struct {
		name, token, key, aud string
		now                   time.Time
		ok                    bool
	}{{"valid", token, key, "news-service", now, true}, {"tampered", token + "x", key, "news-service", now, false}, {"audience", token, key, "company-service", now, false}, {"wrong key", token, strings.Repeat("x", 32), "news-service", now, false}, {"expired", token, key, "news-service", now.Add(2 * time.Minute), false}} {
		t.Run(tc.name, func(t *testing.T) {
			_, e := Verify(tc.token, tc.key, tc.aud, tc.now)
			if (e == nil) != tc.ok {
				t.Fatalf("unexpected result: %v", e)
			}
		})
	}
	interceptor := AuthInterceptor("news-service", key)
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+token))
	_, e := interceptor(ctx, nil, nil, func(c context.Context, _ any) (any, error) {
		if RequireCompany(c, "b") == nil {
			t.Fatal("foreign company accepted")
		}
		return nil, RequireCompany(c, "a")
	})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = interceptor(context.Background(), nil, nil, func(context.Context, any) (any, error) { t.Fatal("unsigned call reached handler"); return nil, nil }); e == nil {
		t.Fatal("unsigned call accepted")
	}
}
func TestDecodeStrict(t *testing.T) {
	for _, s := range []string{`{"x":1} {"x":2}`, `{"extra":1}`, `{"x":1} garbage`} {
		var v struct {
			X int `json:"x"`
		}
		r := httptest.NewRequest("POST", "/", strings.NewReader(s))
		if Decode(httptest.NewRecorder(), r, &v) == nil {
			t.Fatalf("accepted %q", s)
		}
	}
	b, _ := json.Marshal(map[string]int{"x": 1})
	var v struct {
		X int `json:"x"`
	}
	if e := Decode(httptest.NewRecorder(), httptest.NewRequest("POST", "/", strings.NewReader(string(b))), &v); e != nil {
		t.Fatal(e)
	}
}
