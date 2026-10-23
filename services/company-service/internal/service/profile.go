package service

import (
	"alfa.local/shared/model"
	"errors"
	"math"
	"strings"
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
	if p.Coverage != nil && !unit(*p.Coverage) {
		return errors.New("invalid coverage")
	}
	for _, v := range p.FactorExposure {
		if v != nil && !unit(*v) {
			return errors.New("invalid exposure")
		}
	}
	if p.Features != nil {
		for _, side := range []*model.SideFeatures{p.Features.Buyers, p.Features.Suppliers} {
			if err := validateSide(side); err != nil {
				return err
			}
		}
	}
	return nil
}

func unit(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) && v >= 0 && v <= 1 }

func validateSide(side *model.SideFeatures) error {
	if side == nil {
		return nil
	}
	if side.SectorShares == nil || len(side.SectorShares) > 10 {
		return errors.New("invalid sector shares")
	}
	seen := map[string]bool{}
	sum := 0.0
	for _, item := range side.SectorShares {
		if strings.TrimSpace(item.Sector) == "" || seen[item.Sector] || item.Share == nil || !unit(*item.Share) || *item.Share == 0 {
			return errors.New("invalid sector share")
		}
		seen[item.Sector] = true
		sum += *item.Share
	}
	// Top-ten shares may sum below one. Allow independent rounding to 3 decimals.
	if sum > 1+float64(len(side.SectorShares))*0.0005+1e-9 {
		return errors.New("sector shares exceed one")
	}
	if c := side.Concentration; c != nil {
		for _, v := range []*float64{c.Top1Share, c.Top5Share, c.HHI} {
			if v == nil || !unit(*v) {
				return errors.New("invalid concentration")
			}
		}
		if *c.Top5Share+0.0001 < *c.Top1Share || *c.HHI > *c.Top1Share+0.0001 {
			return errors.New("inconsistent concentration")
		}
	}
	return nil
}
