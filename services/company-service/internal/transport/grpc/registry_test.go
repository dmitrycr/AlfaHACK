package transport

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"alfa.local/company-service/internal/service"
	"alfa.local/shared/model"
	"alfa.local/shared/platform"
	"alfa.local/shared/rpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestCompanyChecksRequireRegistrationIdentity(t *testing.T) {
	s := Server{Registry: service.NewMockRegistry()}
	key := strings.Repeat("k", 32)
	payload, _ := json.Marshal(model.CompanyCheckInput{INN: "9900000017", Name: "Тест Металл Альфа"})
	for _, tc := range []struct {
		sub     string
		service bool
		want    codes.Code
	}{{"regular-user", false, codes.PermissionDenied}, {"other-service", true, codes.PermissionDenied}, {"api-gateway-registration", true, codes.OK}} {
		claims := platform.Claims{Subject: tc.sub, Service: tc.service, Audience: "company-service", Expires: time.Now().Add(time.Minute).Unix()}
		ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+platform.Sign(claims, key)))
		_, e := platform.AuthInterceptor("company-service", key)(ctx, nil, nil, func(ctx context.Context, _ any) (any, error) {
			return s.CheckCompany(ctx, &rpc.Request{Payload: payload})
		})
		if status.Code(e) != tc.want {
			t.Fatalf("%s: %v", tc.sub, e)
		}
	}
}
