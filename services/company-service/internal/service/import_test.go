package service

import (
	"alfa.local/shared/model"
	"encoding/json"
	"os"
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

func TestImportMLFeaturesAndAllProducts(t *testing.T) {
	b, err := os.ReadFile("../../../../docs/api/examples/profile-features.json")
	if err != nil {
		t.Fatal(err)
	}
	csv := "id,name,okved_section,okved_class,okved_group,sector,region,rko,guarantee,loan,leasing,deposit,zpp,factoring\nC_EXAMPLE,Example,F,41,41.20,Construction,Moscow,true,false,,true,false,true,true\n"
	companies, profiles, err := ReadImport(strings.NewReader(csv), strings.NewReader("["+string(b)+"]"))
	if err != nil {
		t.Fatal(err)
	}
	if len(companies[0].Products) != 7 || companies[0].Products["loan"] != nil || !*companies[0].Products["zpp"] || *companies[0].Products["guarantee"] {
		t.Fatal("product flags lost", companies[0].Products)
	}
	if profiles[0].Features == nil || *profiles[0].Features.Buyers.Concentration.HHI != .09 {
		t.Fatal("features lost")
	}
	bad := strings.Replace(string(b), `"hhi": 0.09`, `"hhi": 0.09, "raw_payment": 1`, 1)
	if _, _, err = ReadImport(strings.NewReader(csv), strings.NewReader("["+bad+"]")); err == nil {
		t.Fatal("unknown nested feature accepted")
	}
}
func TestImportSizeLimitCannotSilentlyTruncate(t *testing.T) {
	if _, e := boundedImport(strings.NewReader(strings.Repeat(" ", (10<<20)+1))); e == nil {
		t.Fatal("oversized import accepted")
	}
}
