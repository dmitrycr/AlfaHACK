package postgres

import (
	"alfa.local/api-gateway/internal/service"
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
	"time"
)

type Store struct{ Pool *pgxpool.Pool }

func (s Store) FindUser(ctx context.Context, email string) (service.User, error) {
	var u service.User
	e := s.Pool.QueryRow(ctx, "SELECT id,email,company_id,password_hash,full_name,job_title,email_verified,access_status FROM auth.users WHERE email=$1", email).Scan(&u.ID, &u.Email, &u.CompanyID, &u.PasswordHash, &u.FullName, &u.JobTitle, &u.EmailVerified, &u.AccessStatus)
	if errors.Is(e, pgx.ErrNoRows) {
		e = service.ErrCredentials
	}
	return u, e
}
func (s Store) AllowAttempt(ctx context.Context, key string, limit int, now time.Time) (bool, error) {
	var attempts int
	e := s.Pool.QueryRow(ctx, `INSERT INTO auth.login_attempts(bucket_key,minute,attempts) VALUES($1,$2,1) ON CONFLICT(bucket_key,minute) DO UPDATE SET attempts=auth.login_attempts.attempts+1 RETURNING attempts`, key, now.Unix()/60).Scan(&attempts)
	return attempts <= limit, e
}
func (s Store) CreateSession(ctx context.Context, v service.Session, old string) error {
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, "DELETE FROM auth.sessions WHERE token_hash=$1", old); e != nil {
		return e
	}
	_, e = tx.Exec(ctx, "INSERT INTO auth.sessions(token_hash,user_id,csrf_hash,created_at,last_seen_at,expires_at) VALUES($1,$2,$3,$4,$4,$5)", v.TokenHash, v.User.ID, v.CSRFHash, v.CreatedAt, v.ExpiresAt)
	if e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (s Store) Session(ctx context.Context, hash string, now time.Time, idle time.Duration) (service.Session, error) {
	var v service.Session
	e := s.Pool.QueryRow(ctx, `UPDATE auth.sessions AS s SET last_seen_at=$2 FROM auth.users u WHERE s.token_hash=$1 AND u.id=s.user_id AND s.expires_at>$2 AND s.last_seen_at>$3 AND u.access_status<>'blocked' RETURNING s.token_hash,s.csrf_hash,s.created_at,s.last_seen_at,s.expires_at,u.id,u.email,u.company_id,u.full_name,u.job_title,u.email_verified,u.access_status`, hash, now, now.Add(-idle)).Scan(&v.TokenHash, &v.CSRFHash, &v.CreatedAt, &v.LastSeen, &v.ExpiresAt, &v.User.ID, &v.User.Email, &v.User.CompanyID, &v.User.FullName, &v.User.JobTitle, &v.User.EmailVerified, &v.User.AccessStatus)
	if errors.Is(e, pgx.ErrNoRows) {
		e = service.ErrSession
	}
	return v, e
}
func (s Store) DeleteSession(ctx context.Context, hash string) error {
	_, e := s.Pool.Exec(ctx, "DELETE FROM auth.sessions WHERE token_hash=$1", hash)
	return e
}
func (s Store) Seed(ctx context.Context, password string) error {
	if len(password) < 12 || len(password) > 72 {
		return errors.New("demo password must contain 12..72 bytes")
	}
	hash, e := bcrypt.GenerateFromPassword([]byte(password), 12)
	if e != nil {
		return e
	}
	for _, u := range []service.User{{ID: "user-metal", Email: "metal@example.test", CompanyID: "company-metal"}, {ID: "user-metal-2", Email: "metal2@example.test", CompanyID: "company-metal"}, {ID: "user-build", Email: "build@example.test", CompanyID: "company-build"}, {ID: "user-import", Email: "import@example.test", CompanyID: "company-import"}, {ID: "user-agro", Email: "agro@example.test", CompanyID: "company-agro"}} {
		if _, e = s.Pool.Exec(ctx, "INSERT INTO auth.users(id,email,company_id,password_hash) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING", u.ID, u.Email, u.CompanyID, hash); e != nil {
			return e
		}
	}
	return nil
}
func (s Store) Cleanup(ctx context.Context) error {
	_, e := s.Pool.Exec(ctx, "DELETE FROM auth.sessions WHERE expires_at<now(); DELETE FROM auth.login_attempts WHERE minute < EXTRACT(EPOCH FROM now())::bigint / 60 - 1440")
	return e
}
