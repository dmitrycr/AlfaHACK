package postgres

import (
	"alfa.local/shared/model"
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
)

// Import writes the entire validated batch atomically. Identical batches are idempotent.
func (s Store) Import(ctx context.Context, companies []model.Company, profiles []model.Profile) error {
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext('company-import'))"); e != nil {
		return e
	}
	byID := map[string]model.Profile{}
	for _, p := range profiles {
		byID[p.CompanyID] = p
	}
	for _, c := range companies {
		profile, ok := byID[c.ID]
		if !ok {
			return errors.New("missing company profile")
		}
		companyJSON, _ := json.Marshal(c)
		profileJSON, _ := json.Marshal(profile)
		if _, e = tx.Exec(ctx, "INSERT INTO company.companies(id,document) VALUES($1,$2) ON CONFLICT DO NOTHING", c.ID, companyJSON); e != nil {
			return e
		}
		var id string
		if e = tx.QueryRow(ctx, "SELECT id FROM company.companies WHERE id=$1 FOR UPDATE", c.ID).Scan(&id); e != nil {
			return e
		}
		var latest int
		var existing []byte
		e = tx.QueryRow(ctx, "SELECT version,document FROM company.profiles WHERE company_id=$1 ORDER BY version DESC LIMIT 1", c.ID).Scan(&latest, &existing)
		if e != nil && !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		if latest == profile.Version {
			var previous model.Profile
			if json.Unmarshal(existing, &previous) != nil {
				return errors.New("invalid stored profile")
			}
			canonical, _ := json.Marshal(previous)
			var sameCompany bool
			if e = tx.QueryRow(ctx, "SELECT document=$2::jsonb FROM company.companies WHERE id=$1", id, companyJSON).Scan(&sameCompany); e != nil {
				return e
			}
			if sameCompany && string(canonical) == string(profileJSON) {
				continue
			}
			return ErrConflict
		}
		if profile.Version != latest+1 {
			return ErrConflict
		}
		if _, e = tx.Exec(ctx, "UPDATE company.companies SET document=$2 WHERE id=$1", id, companyJSON); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, "INSERT INTO company.profiles(company_id,version,document) VALUES($1,$2,$3)", id, profile.Version, profileJSON); e != nil {
			return e
		}
	}
	return tx.Commit(ctx)
}
