package service

import (
	"alfa.local/shared/model"
	"errors"
	"math"
)

func ValidateProfile(p model.Profile) error {
	if p.CompanyID == "" || p.Version < 1 || p.RulesetVersion == "" || p.BuiltAt.IsZero() {
		return errors.New("missing profile metadata")
	}
	if p.Channels == nil || p.FactorExposure == nil {
		return errors.New("profile collections are required")
	}
	allowed := map[string]bool{"industry": true, "competitor": true, "demand": true, "supply": true, "financial": true, "currency": true, "commodity": true, "regional": true, "regulatory": true}
	seen := map[string]bool{}
	for _, c := range p.Channels {
		key := c.Name + ":" + c.Sector
		if seen[key] {
			return errors.New("duplicate channel")
		}
		seen[key] = true
		if !allowed[c.Name] || math.IsNaN(c.Weight) || c.Weight < 0 || c.Weight > 1 || (c.Source != "data" && c.Source != "matrix" && c.Source != "llm") || len(c.Evidence) == 0 {
			return errors.New("invalid channel")
		}
	}
	if p.Coverage != nil && (*p.Coverage < 0 || *p.Coverage > 1) {
		return errors.New("invalid coverage")
	}
	for _, v := range p.FactorExposure {
		if v != nil && (*v < 0 || *v > 1) {
			return errors.New("invalid exposure")
		}
	}
	return nil
}
