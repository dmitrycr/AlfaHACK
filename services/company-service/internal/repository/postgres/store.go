package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"alfa.local/shared/model"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrConflict = errors.New("profile version conflict")

type Store struct{ Pool *pgxpool.Pool }

func (s Store) Company(ctx context.Context, id string) (model.Company, error) {
	var c model.Company
	var b []byte
	e := s.Pool.QueryRow(ctx, "SELECT document FROM company.companies WHERE id=$1", id).Scan(&b)
	if e != nil {
		return c, e
	}
	e = json.Unmarshal(b, &c)
	return c, e
}
func (s Store) Profile(ctx context.Context, id string) (model.Profile, error) {
	var p model.Profile
	var b []byte
	e := s.Pool.QueryRow(ctx, "SELECT document FROM company.profiles WHERE company_id=$1 ORDER BY version DESC LIMIT 1", id).Scan(&b)
	if e != nil {
		return p, e
	}
	e = json.Unmarshal(b, &p)
	return p, e
}
func (s Store) Replace(ctx context.Context, p model.Profile) error {
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	var id string
	if e = tx.QueryRow(ctx, "SELECT id FROM company.companies WHERE id=$1 FOR UPDATE", p.CompanyID).Scan(&id); e != nil {
		return e
	}
	var last int
	if e = tx.QueryRow(ctx, "SELECT COALESCE(MAX(version),0) FROM company.profiles WHERE company_id=$1", id).Scan(&last); e != nil {
		return e
	}
	if p.Version != last+1 {
		return ErrConflict
	}
	b, _ := json.Marshal(p)
	_, e = tx.Exec(ctx, "INSERT INTO company.profiles(company_id,version,document) VALUES($1,$2,$3)", id, p.Version, b)
	if e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (s Store) Seed(ctx context.Context) error {
	yes, no := true, false
	data := []struct {
		id, name, sector, section, cls, group, region string
		loan, ved                                     bool
	}{
		{"company-metal", "Демо Металл", "Металлургия", "C", "24", "24.10", "Липецкая область", true, false},
		{"company-build", "Демо Стройка", "Строительство", "F", "41", "41.20", "Москва", true, false},
		{"company-import", "Демо Импорт", "Торговля", "G", "46", "46.90", "Москва", false, true},
		{"company-agro", "Демо Агро", "Сельское хозяйство", "A", "01", "01.11", "Краснодарский край", true, false},
	}
	for _, d := range data {
		loan := no
		if d.loan {
			loan = yes
		}
		revenue := int64(2_000_000_000_000)
		c := model.Company{ID: d.id, Name: d.name, OKVED: model.OKVED{Section: d.section, Class: d.cls, Group: d.group}, Sector: d.sector, Region: d.region, RevenueMinor: &revenue, Products: map[string]*bool{"loan": &loan, "leasing": nil, "deposit": &no}, VED: map[string]any{"fts_flag": d.ved}}
		b, _ := json.Marshal(c)
		if _, e := s.Pool.Exec(ctx, "INSERT INTO company.companies(id,document) VALUES($1,$2) ON CONFLICT DO NOTHING", c.ID, b); e != nil {
			return e
		}
		p := model.Profile{CompanyID: d.id, Version: 1, RulesetVersion: "synthetic-v1", Channels: []model.Channel{{Name: "industry", Sector: d.sector, Weight: 1, Source: "data", Evidence: []string{fmt.Sprintf("synthetic:okved:%s", d.group)}}}, FactorExposure: map[string]*float64{"interest_rate": nil, "CNY": nil}, BuiltAt: time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC), Synthetic: true}
		if d.id == "company-metal" {
			p.Channels = append(p.Channels, model.Channel{Name: "demand", Sector: "Строительство", Weight: 0.7, Source: "matrix", Evidence: []string{"synthetic:construction_buys_steel"}})
		}
		b, _ = json.Marshal(p)
		if _, e := s.Pool.Exec(ctx, "INSERT INTO company.profiles(company_id,version,document) VALUES($1,1,$2) ON CONFLICT DO NOTHING", p.CompanyID, b); e != nil && !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
	}
	return nil
}
