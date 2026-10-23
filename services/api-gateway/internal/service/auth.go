package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"alfa.local/shared/platform"
	"golang.org/x/crypto/bcrypt"
)

var ErrCredentials = errors.New("invalid_credentials")
var ErrSession = errors.New("invalid_session")
var ErrUnavailable = errors.New("authentication_unavailable")
var ErrRateLimited = errors.New("too_many_attempts")
var ErrAccessBlocked = errors.New("access_blocked")

type User struct {
	ID            string `json:"id"`
	Email         string `json:"email"`
	CompanyID     string `json:"company_id"`
	FullName      string `json:"full_name"`
	JobTitle      string `json:"job_title"`
	EmailVerified bool   `json:"email_verified"`
	AccessStatus  string `json:"access_status"`
	PasswordHash  []byte `json:"-"`
}
type Session struct {
	TokenHash string
	CSRFHash  string
	User      User
	CreatedAt time.Time
	LastSeen  time.Time
	ExpiresAt time.Time
}
type AuthStore interface {
	FindUser(context.Context, string) (User, error)
	AllowAttempt(context.Context, string, int, time.Time) (bool, error)
	CreateSession(context.Context, Session, string) error
	Session(context.Context, string, time.Time, time.Duration) (Session, error)
	DeleteSession(context.Context, string) error
}
type Auth struct {
	Store AuthStore
	Now   func() time.Time
	TTL   time.Duration
	Idle  time.Duration
	dummy []byte
}

func NewAuth(store AuthStore) *Auth {
	dummy, _ := bcrypt.GenerateFromPassword([]byte(platform.ID()), 12)
	return &Auth{Store: store, Now: time.Now, TTL: 8 * time.Hour, Idle: 30 * time.Minute, dummy: dummy}
}
func (a *Auth) Login(ctx context.Context, email, password, ip, previous string) (Session, string, string, error) {
	var session Session
	email = strings.ToLower(strings.TrimSpace(email))
	now := a.Now()
	for _, v := range []struct {
		key   string
		limit int
	}{{"ip:" + ip, 20}, {"account:" + email, 10}} {
		ok, e := a.Store.AllowAttempt(ctx, platform.Hash(v.key), v.limit, now)
		if e != nil {
			return session, "", "", e
		}
		if !ok {
			return session, "", "", ErrRateLimited
		}
	}
	if len(email) > 254 || len(password) > 72 || password == "" {
		return session, "", "", ErrCredentials
	}
	user, e := a.Store.FindUser(ctx, email)
	hash := a.dummy
	if e == nil {
		hash = user.PasswordHash
	}
	compareErr := bcrypt.CompareHashAndPassword(hash, []byte(password))
	if e != nil && !errors.Is(e, ErrCredentials) {
		return session, "", "", ErrUnavailable
	}
	if e != nil || compareErr != nil {
		return session, "", "", ErrCredentials
	}
	if user.AccessStatus == "blocked" {
		return session, "", "", ErrAccessBlocked
	}
	token, csrf := platform.ID()+platform.ID(), platform.ID()+platform.ID()
	session = Session{TokenHash: platform.Hash(token), CSRFHash: platform.Hash(csrf), User: user, CreatedAt: now, LastSeen: now, ExpiresAt: now.Add(a.TTL)}
	e = a.Store.CreateSession(ctx, session, platform.Hash(previous))
	return session, token, csrf, e
}
func (a *Auth) Authenticate(ctx context.Context, token string) (Session, error) {
	if len(token) != 64 {
		return Session{}, ErrSession
	}
	s, e := a.Store.Session(ctx, platform.Hash(token), a.Now(), a.Idle)
	if e != nil {
		if !errors.Is(e, ErrSession) {
			return Session{}, ErrUnavailable
		}
		return Session{}, ErrSession
	}
	return s, nil
}
