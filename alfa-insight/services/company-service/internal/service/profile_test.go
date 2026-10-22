package service

import (
	"alfa.local/shared/model"
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
