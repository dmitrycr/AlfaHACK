package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"alfa.local/shared/model"
)

type unavailableRegistry struct{}

func (unavailableRegistry) Lookup(context.Context, string) (RegistryCompany, error) {
	return RegistryCompany{}, errors.New("timeout")
}
func (unavailableRegistry) Provider() string { return "mock-registry-v1" }

func TestCompanyRegistry(t *testing.T) {
	registry := NewMockRegistry()
	now := time.Now()
	for _, tc := range []struct {
		name, inn, company string
		want               error
	}{
		{"canonical", "9900000017", "ООО «Тест Металл Альфа»", nil},
		{"case quotes whitespace", "9900000017", `  ооо  "ТЕСТ Металл Альфа"  `, nil},
		{"alias", "9900000017", "Тест Металл Альфа", nil},
		{"full legal alias", "9900000017", "Общество с ограниченной ответственностью «Тест Металл Альфа»", nil},
		{"mismatch", "9900000017", "Тест Строй Альфа", ErrNameMismatch},
		{"no fuzzy match", "9900000017", "Тест Метал Альфа", ErrNameMismatch},
		{"unknown", "9900000056", "Тест неизвестная", ErrCompanyNotFound},
		{"checksum", "9900000018", "Тест Металл Альфа", ErrInvalidINN},
		{"zero", "0000000000", "Тест Металл Альфа", ErrInvalidINN},
		{"12 digits unsupported", "990000001700", "Тест Металл Альфа", ErrInvalidINN},
		{"nonascii digits", "９９００００００１７", "Тест Металл Альфа", ErrInvalidINN},
		{"empty name", "9900000017", " ", ErrInvalidName},
		{"controls", "9900000017", "Тест\x00Металл", ErrInvalidName},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, e := CheckCompany(context.Background(), registry, model.CompanyCheckInput{INN: tc.inn, Name: tc.company}, now)
			if !errors.Is(e, tc.want) {
				t.Fatalf("got %v want %v", e, tc.want)
			}
			if e == nil && (got.Status != "matched_mock" || got.Provider != "mock-registry-v1" || got.CheckedAt.IsZero()) {
				t.Fatalf("unlabelled mock result: %+v", got)
			}
		})
	}
	if _, e := CheckCompany(context.Background(), unavailableRegistry{}, model.CompanyCheckInput{INN: "9900000017", Name: "Тест Металл Альфа"}, now); !errors.Is(e, ErrRegistryUnavailable) {
		t.Fatal(e)
	}
}
