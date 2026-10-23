package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/mail"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"alfa.local/shared/model"
	"alfa.local/shared/platform"
	"golang.org/x/crypto/bcrypt"
)

var ErrRegistrationInput = errors.New("invalid_registration")
var ErrEmailExists = errors.New("email_already_registered")
var ErrRegistrationConflict = errors.New("idempotency_conflict")
var ErrVerification = errors.New("invalid_or_expired_verification")
var ErrMembershipState = errors.New("invalid_membership_state")

type RegistrationInput struct {
	FullName    string `json:"full_name"`
	JobTitle    string `json:"job_title"`
	Email       string `json:"email"`
	Password    string `json:"password"`
	CompanyName string `json:"company_name"`
	INN         string `json:"inn"`
}
type RegistrationResult struct {
	User          User               `json:"user"`
	Company       model.CompanyCheck `json:"company_check"`
	EmailDelivery string             `json:"email_delivery"`
	Created       bool               `json:"-"`
}
type PendingRegistration struct {
	User             User
	KeyHash          string
	Fingerprint      string
	VerificationHash string
	MockToken        string
	ExpiresAt        time.Time
	Company          model.CompanyCheck
}
type RegistrationStore interface {
	Registration(context.Context, string, string) (RegistrationResult, bool, error)
	Register(context.Context, PendingRegistration) (RegistrationResult, error)
	VerifyEmail(context.Context, string, time.Time) (User, error)
	ReplaceVerification(context.Context, string, string, string, time.Time) error
}
type CompanyVerifier interface {
	Check(context.Context, model.CompanyCheckInput, bool) (model.CompanyCheck, error)
}
type Registration struct {
	Store     RegistrationStore
	Companies CompanyVerifier
	Key       string
	Now       func() time.Time
}

func normalizeRegistration(in RegistrationInput) (RegistrationInput, error) {
	in.Email = strings.ToLower(strings.TrimSpace(in.Email))
	in.FullName = strings.TrimSpace(in.FullName)
	in.JobTitle = strings.TrimSpace(in.JobTitle)
	in.CompanyName = strings.TrimSpace(in.CompanyName)
	in.INN = strings.TrimSpace(in.INN)
	address, e := mail.ParseAddress(in.Email)
	if e != nil || address.Address != in.Email || len(in.Email) > 254 || !strings.Contains(in.Email, "@") {
		return in, ErrRegistrationInput
	}
	for _, v := range []string{in.FullName, in.JobTitle, in.CompanyName} {
		if !utf8.ValidString(v) || utf8.RuneCountInString(v) < 2 || utf8.RuneCountInString(v) > 300 || strings.ContainsFunc(v, unicode.IsControl) {
			return in, ErrRegistrationInput
		}
	}
	if len(in.Password) < 12 || len(in.Password) > 72 || !utf8.ValidString(in.Password) {
		return in, ErrRegistrationInput
	}
	return in, nil
}
func (r *Registration) Register(ctx context.Context, in RegistrationInput, key string) (RegistrationResult, error) {
	var result RegistrationResult
	in, e := normalizeRegistration(in)
	if e != nil {
		return result, e
	}
	if len(key) < 8 || len(key) > 128 {
		return result, ErrRegistrationInput
	}
	b, _ := json.Marshal(in)
	mac := hmac.New(sha256.New, []byte(r.Key))
	mac.Write(b)
	fingerprint := hex.EncodeToString(mac.Sum(nil))
	keyHash := platform.Hash(in.Email + ":" + key)
	previous, exists, e := r.Store.Registration(ctx, keyHash, fingerprint)
	if e != nil || exists {
		return previous, e
	}
	company, e := r.Companies.Check(ctx, model.CompanyCheckInput{INN: in.INN, Name: in.CompanyName}, true)
	if e != nil {
		return result, e
	}
	hash, e := bcrypt.GenerateFromPassword([]byte(in.Password), 12)
	if e != nil {
		return result, e
	}
	token := platform.ID() + platform.ID()
	user := User{ID: platform.ID(), Email: in.Email, FullName: in.FullName, JobTitle: in.JobTitle, CompanyID: company.CompanyID, PasswordHash: hash, EmailVerified: false, AccessStatus: "pending_email"}
	return r.Store.Register(ctx, PendingRegistration{User: user, KeyHash: keyHash, Fingerprint: fingerprint, VerificationHash: platform.Hash(token), MockToken: token, ExpiresAt: r.Now().Add(30 * time.Minute), Company: company})
}
func (r *Registration) Verify(ctx context.Context, token string) (User, error) {
	if len(token) != 64 {
		return User{}, ErrVerification
	}
	return r.Store.VerifyEmail(ctx, platform.Hash(token), r.Now())
}
func (r *Registration) Resend(ctx context.Context, email string) error {
	email = strings.ToLower(strings.TrimSpace(email))
	if len(email) > 254 {
		return ErrRegistrationInput
	}
	token := platform.ID() + platform.ID()
	return r.Store.ReplaceVerification(ctx, email, platform.Hash(token), token, r.Now().Add(30*time.Minute))
}
