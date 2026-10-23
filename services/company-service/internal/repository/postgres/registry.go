package postgres

import (
	"context"
	"encoding/json"
	"strings"

	"alfa.local/shared/model"
)

// EnsureCompany is idempotent by INN. It does not attach users or grant access.
// Repeated calls never overwrite enrichment, profiles or imported company data.
func (s Store) EnsureCompany(ctx context.Context, check model.CompanyCheck, submitted string) (model.CompanyCheck, error) {
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return check, e
	}
	defer tx.Rollback(ctx)
	c := model.Company{ID: check.CompanyID, Name: check.CanonicalName, INN: check.INN, Products: map[string]*bool{}, VED: map[string]any{}}
	b, _ := json.Marshal(c)
	_, e = tx.Exec(ctx, `INSERT INTO company.companies(id,inn,document) VALUES($1,$2,$3) ON CONFLICT(inn) DO NOTHING`, c.ID, c.INN, b)
	if e != nil {
		return check, e
	}
	if e = tx.QueryRow(ctx, `SELECT id FROM company.companies WHERE inn=$1`, check.INN).Scan(&check.CompanyID); e != nil {
		return check, e
	}
	_, e = tx.Exec(ctx, `INSERT INTO company.registry_checks(inn,company_id,submitted_name,canonical_name,provider,checked_at) VALUES($1,$2,$3,$4,$5,$6)
 ON CONFLICT(inn) DO UPDATE SET submitted_name=EXCLUDED.submitted_name,canonical_name=EXCLUDED.canonical_name,provider=EXCLUDED.provider,checked_at=EXCLUDED.checked_at`, check.INN, check.CompanyID, strings.TrimSpace(submitted), check.CanonicalName, check.Provider, check.CheckedAt)
	if e != nil {
		return check, e
	}
	return check, tx.Commit(ctx)
}
