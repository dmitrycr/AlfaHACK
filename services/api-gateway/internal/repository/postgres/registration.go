package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"alfa.local/api-gateway/internal/service"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type rowQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func registration(ctx context.Context, q rowQuerier, key, fingerprint string) (service.RegistrationResult, bool, error) {
	var v service.RegistrationResult
	var saved string
	var check []byte
	e := q.QueryRow(ctx, `SELECT u.id,u.email,u.company_id,u.full_name,u.job_title,u.email_verified,u.access_status,r.fingerprint,r.company_check
 FROM auth.registration_requests r JOIN auth.users u ON u.id=r.user_id WHERE r.key_hash=$1`, key).Scan(&v.User.ID, &v.User.Email, &v.User.CompanyID, &v.User.FullName, &v.User.JobTitle, &v.User.EmailVerified, &v.User.AccessStatus, &saved, &check)
	if errors.Is(e, pgx.ErrNoRows) {
		return v, false, nil
	}
	if e != nil {
		return v, false, e
	}
	if saved != fingerprint {
		return v, true, service.ErrRegistrationConflict
	}
	e = json.Unmarshal(check, &v.Company)
	v.EmailDelivery = "mock"
	return v, true, e
}
func (s Store) Registration(ctx context.Context, key, fingerprint string) (service.RegistrationResult, bool, error) {
	return registration(ctx, s.Pool, key, fingerprint)
}
func (s Store) Register(ctx context.Context, in service.PendingRegistration) (service.RegistrationResult, error) {
	var v service.RegistrationResult
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return v, e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "registration:"+in.User.Email); e != nil {
		return v, e
	}
	previous, exists, e := registration(ctx, tx, in.KeyHash, in.Fingerprint)
	if e != nil || exists {
		return previous, e
	}
	u := in.User
	_, e = tx.Exec(ctx, `INSERT INTO auth.users(id,email,company_id,password_hash,full_name,job_title,email_verified,access_status) VALUES($1,$2,$3,$4,$5,$6,false,'pending_email')`, u.ID, u.Email, u.CompanyID, u.PasswordHash, u.FullName, u.JobTitle)
	if e != nil {
		var pg *pgconn.PgError
		if errors.As(e, &pg) && pg.Code == "23505" {
			return v, service.ErrEmailExists
		}
		return v, e
	}
	check, _ := json.Marshal(in.Company)
	if _, e = tx.Exec(ctx, `INSERT INTO auth.registration_requests(key_hash,fingerprint,user_id,company_check) VALUES($1,$2,$3,$4)`, in.KeyHash, in.Fingerprint, u.ID, check); e != nil {
		return v, e
	}
	if e = saveVerification(ctx, tx, u.ID, in.VerificationHash, in.MockToken, in.ExpiresAt); e != nil {
		return v, e
	}
	v = service.RegistrationResult{User: u, Company: in.Company, EmailDelivery: "mock", Created: true}
	return v, tx.Commit(ctx)
}
func saveVerification(ctx context.Context, tx pgx.Tx, id, hash, token string, expires time.Time) error {
	_, e := tx.Exec(ctx, `INSERT INTO auth.email_verifications(user_id,token_hash,expires_at) VALUES($1,$2,$3) ON CONFLICT(user_id) DO UPDATE SET token_hash=EXCLUDED.token_hash,expires_at=EXCLUDED.expires_at`, id, hash, expires)
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `INSERT INTO auth.mock_mail(user_id,token,expires_at) VALUES($1,$2,$3) ON CONFLICT(user_id) DO UPDATE SET token=EXCLUDED.token,expires_at=EXCLUDED.expires_at,created_at=now()`, id, token, expires)
	return e
}
func (s Store) VerifyEmail(ctx context.Context, hash string, now time.Time) (service.User, error) {
	var u service.User
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return u, e
	}
	defer tx.Rollback(ctx)
	var id string
	if e = tx.QueryRow(ctx, `SELECT user_id FROM auth.email_verifications WHERE token_hash=$1`, hash).Scan(&id); e != nil {
		if errors.Is(e, pgx.ErrNoRows) {
			e = service.ErrVerification
		}
		return u, e
	}
	var status string
	if e = tx.QueryRow(ctx, `SELECT access_status FROM auth.users WHERE id=$1 FOR UPDATE`, id).Scan(&status); e != nil {
		return u, e
	}
	if status != "pending_email" {
		return u, service.ErrVerification
	}
	var consumed string
	e = tx.QueryRow(ctx, `DELETE FROM auth.email_verifications WHERE user_id=$1 AND token_hash=$2 AND expires_at>$3 RETURNING user_id`, id, hash, now).Scan(&consumed)
	if e != nil {
		if errors.Is(e, pgx.ErrNoRows) {
			e = service.ErrVerification
		}
		return u, e
	}
	e = tx.QueryRow(ctx, `UPDATE auth.users SET email_verified=true,access_status='pending_approval' WHERE id=$1 RETURNING id,email,company_id,full_name,job_title,email_verified,access_status`, id).Scan(&u.ID, &u.Email, &u.CompanyID, &u.FullName, &u.JobTitle, &u.EmailVerified, &u.AccessStatus)
	if e != nil {
		return u, e
	}
	if _, e = tx.Exec(ctx, `DELETE FROM auth.mock_mail WHERE user_id=$1`, id); e != nil {
		return u, e
	}
	return u, tx.Commit(ctx)
}
func (s Store) ReplaceVerification(ctx context.Context, email, hash, token string, expires time.Time) error {
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	var id string
	e = tx.QueryRow(ctx, `SELECT id FROM auth.users WHERE email=$1 AND NOT email_verified AND access_status='pending_email' FOR UPDATE`, email).Scan(&id)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil
	}
	if e != nil {
		return e
	}
	if e = saveVerification(ctx, tx, id, hash, token, expires); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (s Store) MockVerificationToken(ctx context.Context, email string) (string, error) {
	var token string
	e := s.Pool.QueryRow(ctx, `SELECT m.token FROM auth.mock_mail m JOIN auth.users u ON u.id=m.user_id WHERE u.email=$1 AND m.expires_at>now() AND u.access_status='pending_email'`, strings.ToLower(strings.TrimSpace(email))).Scan(&token)
	return token, e
}

// DecideMembership is available only to an operator with local auth DB credentials.
// No user-supplied title or public endpoint can approve membership.
func (s Store) DecideMembership(ctx context.Context, id, operator string, approve bool) error {
	if strings.TrimSpace(operator) == "" || len(operator) > 200 {
		return service.ErrMembershipState
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	var state string
	var verified bool
	if e = tx.QueryRow(ctx, `SELECT access_status,email_verified FROM auth.users WHERE id=$1 FOR UPDATE`, id).Scan(&state, &verified); e != nil {
		return e
	}
	action, next := "revoke", "blocked"
	if approve {
		if !verified || state != "pending_approval" {
			return service.ErrMembershipState
		}
		action, next = "approve", "active"
	}
	if _, e = tx.Exec(ctx, `UPDATE auth.users SET access_status=$2 WHERE id=$1`, id, next); e != nil {
		return e
	}
	if !approve {
		if _, e = tx.Exec(ctx, `DELETE FROM auth.sessions WHERE user_id=$1`, id); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `DELETE FROM auth.email_verifications WHERE user_id=$1`, id); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `DELETE FROM auth.mock_mail WHERE user_id=$1`, id); e != nil {
			return e
		}
	}
	if _, e = tx.Exec(ctx, `INSERT INTO auth.membership_decisions(user_id,operator,action) VALUES($1,$2,$3)`, id, operator, action); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
