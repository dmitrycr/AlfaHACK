package service

import (
	"alfa.local/shared/model"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestNormalizedCSVImport(t *testing.T) {
	profile := model.Profile{CompanyID: "new-company", Version: 1, RulesetVersion: "test-v1", BuiltAt: time.Now(), Channels: []model.Channel{}, FactorExposure: map[string]*float64{}, Synthetic: true}
	b, _ := json.Marshal([]model.Profile{profile})
	csv := "id,name,okved_section,okved_class,okved_group,sector,region,loan,deposit,revenue_minor\nnew-company,Демо,C,24,24.10,Металлургия,Москва,,false,2000000000000\n"
	companies, profiles, e := ReadImport(strings.NewReader(csv), strings.NewReader(string(b)))
	if e != nil || len(companies) != 1 || len(profiles) != 1 {
		t.Fatal(companies, profiles, e)
	}
	if companies[0].Products["loan"] != nil || companies[0].Products["deposit"] == nil || *companies[0].Products["deposit"] {
		t.Fatal("unknown and false conflated")
	}
	bad := strings.Replace(csv, ",24.10,", ",XX,", 1)
	if _, _, e = ReadImport(strings.NewReader(bad), strings.NewReader(string(b))); e == nil {
		t.Fatal("invalid OKVED accepted")
	}
}
func TestImportSizeLimitCannotSilentlyTruncate(t *testing.T) {
	if _, e := boundedImport(strings.NewReader(strings.Repeat(" ", (10<<20)+1))); e == nil {
		t.Fatal("oversized import accepted")
	}
}
