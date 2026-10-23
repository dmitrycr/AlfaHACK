package service

import (
	"alfa.local/shared/model"
	"encoding/json"
	"math"
	"os"
	"testing"
	"time"
)

func TestProfileValidation(t *testing.T) {
	p := model.Profile{CompanyID: "c", Version: 1, RulesetVersion: "v1", BuiltAt: time.Now(), Channels: []model.Channel{{Name: "demand", Sector: "construction", Weight: .7, Source: "matrix", Evidence: []string{"synthetic"}}}}
	p.FactorExposure = map[string]*float64{}
	if e := ValidateProfile(p); e != nil {
		t.Fatal(e)
	}
	p.Channels[0].Weight = 1.1
	if ValidateProfile(p) == nil {
		t.Fatal("invalid weight accepted")
	}
	p.Channels[0].Weight = .7
	p.Channels[0].Evidence = nil
	if ValidateProfile(p) == nil {
		t.Fatal("missing evidence accepted")
	}
}

func TestMLFeaturesValidation(t *testing.T) {
	b, err := os.ReadFile("../../../../docs/api/examples/profile-features.json")
	if err != nil {
		t.Fatal(err)
	}
	var original model.Profile
	if err = json.Unmarshal(b, &original); err != nil {
		t.Fatal(err)
	}
	if err = ValidateProfile(original); err != nil {
		t.Fatal(err)
	}
	ptr := func(v float64) *float64 { return &v }
	cases := []struct {
		name   string
		change func(*model.Profile)
	}{
		{"missing share", func(p *model.Profile) { p.Features.Buyers.SectorShares[0].Share = nil }},
		{"negative share", func(p *model.Profile) { p.Features.Buyers.SectorShares[0].Share = ptr(-.1) }},
		{"too much share", func(p *model.Profile) { p.Features.Buyers.SectorShares[0].Share = ptr(.9) }},
		{"duplicate sector", func(p *model.Profile) {
			p.Features.Buyers.SectorShares[1].Sector = p.Features.Buyers.SectorShares[0].Sector
		}},
		{"missing array", func(p *model.Profile) { p.Features.Buyers.SectorShares = nil }},
		{"missing hhi", func(p *model.Profile) { p.Features.Buyers.Concentration.HHI = nil }},
		{"impossible top5", func(p *model.Profile) { p.Features.Buyers.Concentration.Top5Share = ptr(.1) }},
		{"impossible hhi", func(p *model.Profile) { p.Features.Buyers.Concentration.HHI = ptr(.9) }},
		{"nan coverage", func(p *model.Profile) { p.Coverage = ptr(math.NaN()) }},
		{"infinite exposure", func(p *model.Profile) { p.FactorExposure["x"] = ptr(math.Inf(1)) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var p model.Profile
			if err := json.Unmarshal(b, &p); err != nil {
				t.Fatal(err)
			}
			tc.change(&p)
			if ValidateProfile(p) == nil {
				t.Fatal("invalid features accepted")
			}
		})
	}
	original.Features.Buyers = nil
	original.Features.Suppliers.Concentration = nil
	if err = ValidateProfile(original); err != nil {
		t.Fatal("nullable side rejected", err)
	}
	original.Features.Suppliers.SectorShares = []model.SectorShare{{Sector: "A", Share: ptr(.334)}, {Sector: "B", Share: ptr(.334)}, {Sector: "C", Share: ptr(.333)}}
	if err = ValidateProfile(original); err != nil {
		t.Fatal("independent rounding rejected", err)
	}
}
