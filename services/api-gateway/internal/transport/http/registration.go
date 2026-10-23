package transport

import (
	"context"
	"encoding/json"
	"errors"
	"mime"
	"net"
	"net/http"
	"strings"
	"time"

	"alfa.local/api-gateway/internal/service"
	"alfa.local/shared/model"
	"alfa.local/shared/platform"
	"alfa.local/shared/rpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type CompanyVerifier struct{ API *API }

func (v CompanyVerifier) Check(ctx context.Context, input model.CompanyCheckInput, ensure bool) (model.CompanyCheck, error) {
	var check model.CompanyCheck
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ctx = platform.Outgoing(ctx, platform.Claims{Subject: "api-gateway-registration", Audience: "company-service", Service: true, CorrelationID: platform.ID()}, v.API.Key)
	b, _ := json.Marshal(input)
	var response *rpc.Response
	var e error
	if ensure {
		response, e = v.API.Company.EnsureCompany(ctx, &rpc.Request{Payload: b})
	} else {
		response, e = v.API.Company.CheckCompany(ctx, &rpc.Request{Payload: b})
	}
	if e != nil {
		return check, e
	}
	if response == nil || json.Unmarshal(response.Payload, &check) != nil || check.CompanyID == "" || check.INN != strings.TrimSpace(input.INN) || check.Status != "matched_mock" || check.Provider != "mock-registry-v1" || check.CheckedAt.IsZero() {
		return check, status.Error(codes.Unavailable, "registry_unavailable")
	}
	return check, nil
}
func (a *API) registrationRequest(w http.ResponseWriter, r *http.Request, action string, limit int) bool {
	w.Header().Set("Cache-Control", "no-store")
	if !a.origin(w, r) {
		return false
	}
	if a.Registration == nil {
		fail(w, 503, "registration_unavailable")
		return false
	}
	media, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if media != "application/json" {
		fail(w, 415, "json_required")
		return false
	}
	ip, _, e := net.SplitHostPort(r.RemoteAddr)
	if e != nil {
		ip = r.RemoteAddr
	}
	return a.registrationLimit(w, r, "registration:"+action+":ip:"+ip, limit)
}
func (a *API) registrationLimit(w http.ResponseWriter, r *http.Request, key string, limit int) bool {
	ok, e := a.Auth.Store.AllowAttempt(r.Context(), platform.Hash(key), limit, time.Now())
	if e != nil {
		fail(w, 503, "registration_unavailable")
		return false
	}
	if !ok {
		w.Header().Set("Retry-After", "60")
		fail(w, 429, "too_many_attempts")
		return false
	}
	return true
}
func registrationError(w http.ResponseWriter, e error) {
	switch {
	case errors.Is(e, service.ErrRegistrationInput), errors.Is(e, service.ErrVerification):
		fail(w, 400, e.Error())
	case errors.Is(e, service.ErrEmailExists), errors.Is(e, service.ErrRegistrationConflict):
		fail(w, 409, e.Error())
	default:
		switch status.Code(e) {
		case codes.InvalidArgument:
			fail(w, 400, status.Convert(e).Message())
		case codes.FailedPrecondition:
			fail(w, 422, status.Convert(e).Message())
		default:
			fail(w, 503, "registration_unavailable")
		}
	}
}
func (a *API) register(w http.ResponseWriter, r *http.Request) {
	if !a.registrationRequest(w, r, "register", 10) {
		return
	}
	var input service.RegistrationInput
	if e := platform.Decode(w, r, &input); e != nil {
		fail(w, 400, "invalid_input")
		return
	}
	if !a.registrationLimit(w, r, "register:email:"+strings.ToLower(strings.TrimSpace(input.Email)), 3) {
		return
	}
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if len(key) < 8 || len(key) > 128 {
		fail(w, 400, "invalid_idempotency_key")
		return
	}
	result, e := a.Registration.Register(r.Context(), input, key)
	if e != nil {
		registrationError(w, e)
		return
	}
	code := http.StatusOK
	if result.Created {
		code = http.StatusCreated
	}
	platform.JSON(w, code, result)
}
func (a *API) checkCompany(w http.ResponseWriter, r *http.Request) {
	if !a.registrationRequest(w, r, "company-check", 20) {
		return
	}
	var input model.CompanyCheckInput
	if e := platform.Decode(w, r, &input); e != nil {
		fail(w, 400, "invalid_input")
		return
	}
	result, e := a.Registration.Companies.Check(r.Context(), input, false)
	if e != nil {
		registrationError(w, e)
		return
	}
	platform.JSON(w, 200, result)
}
func (a *API) verifyEmail(w http.ResponseWriter, r *http.Request) {
	if !a.registrationRequest(w, r, "verify-email", 20) {
		return
	}
	var input struct {
		Token string `json:"token"`
	}
	if e := platform.Decode(w, r, &input); e != nil {
		fail(w, 400, "invalid_input")
		return
	}
	user, e := a.Registration.Verify(r.Context(), input.Token)
	if e != nil {
		registrationError(w, e)
		return
	}
	platform.JSON(w, 200, map[string]any{"user": user})
}
func (a *API) resendVerification(w http.ResponseWriter, r *http.Request) {
	if !a.registrationRequest(w, r, "resend", 10) {
		return
	}
	var input struct {
		Email string `json:"email"`
	}
	if e := platform.Decode(w, r, &input); e != nil {
		fail(w, 400, "invalid_input")
		return
	}
	if !a.registrationLimit(w, r, "resend:email:"+strings.ToLower(strings.TrimSpace(input.Email)), 3) {
		return
	}
	if e := a.Registration.Resend(r.Context(), input.Email); e != nil {
		registrationError(w, e)
		return
	}
	platform.JSON(w, 202, map[string]string{"status": "accepted", "email_delivery": "mock"})
}
