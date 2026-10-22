package platform

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func ID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
func Hash(v string) string { h := sha256.Sum256([]byte(v)); return hex.EncodeToString(h[:]) }

type Claims struct {
	Subject       string `json:"sub"`
	CompanyID     string `json:"company_id,omitempty"`
	Audience      string `json:"aud"`
	Service       bool   `json:"service"`
	Expires       int64  `json:"exp"`
	CorrelationID string `json:"correlation_id"`
}
type claimsKey struct{}

func Principal(ctx context.Context) Claims { c, _ := ctx.Value(claimsKey{}).(Claims); return c }
func Sign(c Claims, key string) string {
	b, _ := json.Marshal(c)
	payload := base64.RawURLEncoding.EncodeToString(b)
	h := hmac.New(sha256.New, []byte(key))
	h.Write([]byte(payload))
	return payload + "." + base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}
func Verify(token, key, audience string, now time.Time) (Claims, error) {
	var c Claims
	if len(token) > 4096 {
		return c, errors.New("invalid token size")
	}
	p := strings.Split(token, ".")
	if len(p) != 2 {
		return c, errors.New("invalid token")
	}
	sig, e := base64.RawURLEncoding.DecodeString(p[1])
	if e != nil {
		return c, e
	}
	h := hmac.New(sha256.New, []byte(key))
	h.Write([]byte(p[0]))
	if !hmac.Equal(sig, h.Sum(nil)) {
		return c, errors.New("invalid signature")
	}
	b, e := base64.RawURLEncoding.DecodeString(p[0])
	if e != nil {
		return c, e
	}
	if e = json.Unmarshal(b, &c); e != nil {
		return c, e
	}
	if c.Subject == "" || c.Expires <= now.Unix() || c.Expires > now.Add(2*time.Minute).Unix() || c.Audience != audience {
		return c, errors.New("invalid claims")
	}
	return c, nil
}
func Outgoing(ctx context.Context, c Claims, key string) context.Context {
	c.Expires = time.Now().Add(time.Minute).Unix()
	return metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer "+Sign(c, key)))
}
func AuthInterceptor(service, key string) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		md, _ := metadata.FromIncomingContext(ctx)
		values := md.Get("authorization")
		if len(values) != 1 || !strings.HasPrefix(values[0], "Bearer ") {
			return nil, status.Error(codes.Unauthenticated, "authentication required")
		}
		c, e := Verify(strings.TrimPrefix(values[0], "Bearer "), key, service, time.Now())
		if e != nil {
			return nil, status.Error(codes.Unauthenticated, "invalid credentials")
		}
		return handler(context.WithValue(ctx, claimsKey{}, c), req)
	}
}
func RequireCompany(ctx context.Context, id string) error {
	c := Principal(ctx)
	if c.CompanyID != id || id == "" {
		return status.Error(codes.PermissionDenied, "company access denied")
	}
	return nil
}
func Visible(scope, company string) bool {
	return scope == "public" || (company != "" && scope == "company:"+company)
}
