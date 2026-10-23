package service

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"alfa.local/shared/model"
)

var ErrInvalidINN = errors.New("invalid_inn")
var ErrInvalidName = errors.New("invalid_company_name")
var ErrCompanyNotFound = errors.New("company_not_found")
var ErrNameMismatch = errors.New("company_name_mismatch")
var ErrRegistryUnavailable = errors.New("registry_unavailable")

type RegistryCompany struct {
	INN     string   `json:"inn"`
	Name    string   `json:"name"`
	Aliases []string `json:"aliases"`
}

// CompanyRegistry is the boundary for a future external registry adapter.
// Provider identifiers are part of the result: mock checks are never real verification.
type CompanyRegistry interface {
	Lookup(context.Context, string) (RegistryCompany, error)
	Provider() string
}

//go:embed testdata/registry.json
var mockRegistryJSON []byte

type MockRegistry struct{ records map[string]RegistryCompany }

func NewMockRegistry() *MockRegistry {
	var entries []RegistryCompany
	if e := json.Unmarshal(mockRegistryJSON, &entries); e != nil {
		panic(e)
	}
	m := &MockRegistry{records: map[string]RegistryCompany{}}
	for _, c := range entries {
		if !ValidINN(c.INN) {
			panic("invalid mock INN")
		}
		m.records[c.INN] = c
	}
	return m
}
func (*MockRegistry) Provider() string { return "mock-registry-v1" }
func (m *MockRegistry) Lookup(ctx context.Context, inn string) (RegistryCompany, error) {
	if e := ctx.Err(); e != nil {
		return RegistryCompany{}, e
	}
	c, ok := m.records[inn]
	if !ok {
		return c, ErrCompanyNotFound
	}
	return c, nil
}

// Only legal entities are supported in this MVP. No existence claim follows from a checksum.
func ValidINN(inn string) bool {
	if len(inn) != 10 || inn == "0000000000" {
		return false
	}
	for _, c := range inn {
		if c < '0' || c > '9' {
			return false
		}
	}
	sum := 0
	for i, w := range []int{2, 4, 10, 3, 5, 9, 4, 6, 8} {
		sum += int(inn[i]-'0') * w
	}
	return sum%11%10 == int(inn[9]-'0')
}
func normalizedCompanyName(name string) string {
	name = strings.Map(func(r rune) rune {
		switch r {
		case '"', '«', '»', '“', '”', '„':
			return -1
		}
		return unicode.ToLower(r)
	}, name)
	return strings.Join(strings.Fields(name), " ")
}
func CheckCompany(ctx context.Context, registry CompanyRegistry, in model.CompanyCheckInput, now time.Time) (model.CompanyCheck, error) {
	var result model.CompanyCheck
	in.INN = strings.TrimSpace(in.INN)
	in.Name = strings.TrimSpace(in.Name)
	if !ValidINN(in.INN) {
		return result, ErrInvalidINN
	}
	if !utf8.ValidString(in.Name) || utf8.RuneCountInString(in.Name) < 2 || utf8.RuneCountInString(in.Name) > 300 || normalizedCompanyName(in.Name) == "" || strings.ContainsFunc(in.Name, unicode.IsControl) {
		return result, ErrInvalidName
	}
	if registry == nil {
		return result, ErrRegistryUnavailable
	}
	company, e := registry.Lookup(ctx, in.INN)
	if e != nil {
		if errors.Is(e, ErrCompanyNotFound) {
			return result, e
		}
		return result, ErrRegistryUnavailable
	}
	if company.INN != in.INN || company.Name == "" {
		return result, ErrRegistryUnavailable
	}
	match := normalizedCompanyName(in.Name) == normalizedCompanyName(company.Name)
	for _, alias := range company.Aliases {
		match = match || normalizedCompanyName(in.Name) == normalizedCompanyName(alias)
	}
	if !match {
		return result, ErrNameMismatch
	}
	result = model.CompanyCheck{CompanyID: "company-inn-" + in.INN, INN: in.INN, CanonicalName: company.Name, Status: "matched_mock", Provider: registry.Provider(), CheckedAt: now.UTC()}
	// External adapters must map their evidence to a real verification status explicitly.
	if registry.Provider() != "mock-registry-v1" {
		return model.CompanyCheck{}, ErrRegistryUnavailable
	}
	return result, nil
}
