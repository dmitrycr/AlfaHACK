package service

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"

	"alfa.local/shared/model"
)

func ValidateCompany(c model.Company) error {
	if !regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`).MatchString(c.ID) || strings.TrimSpace(c.Name) == "" || strings.TrimSpace(c.Sector) == "" || strings.TrimSpace(c.Region) == "" {
		return errors.New("invalid company identity")
	}
	if !regexp.MustCompile(`^[A-U]$`).MatchString(c.OKVED.Section) || !regexp.MustCompile(`^[0-9]{2}$`).MatchString(c.OKVED.Class) || !regexp.MustCompile(`^[0-9]{2}(\.[0-9]{2})?$`).MatchString(c.OKVED.Group) || !strings.HasPrefix(c.OKVED.Group, c.OKVED.Class) {
		return errors.New("invalid normalized OKVED")
	}
	if c.RevenueMinor != nil && *c.RevenueMinor < 0 {
		return errors.New("negative revenue")
	}
	return nil
}
func ReadImport(companies io.Reader, profiles io.Reader) ([]model.Company, []model.Profile, error) {
	companyBytes, e := boundedImport(companies)
	if e != nil {
		return nil, nil, e
	}
	profileBytes, e := boundedImport(profiles)
	if e != nil {
		return nil, nil, e
	}
	r := csv.NewReader(bytes.NewReader(companyBytes))
	header, e := r.Read()
	if e != nil {
		return nil, nil, e
	}
	positions := map[string]int{}
	allowed := map[string]bool{}
	for _, name := range []string{"id", "name", "okved_section", "okved_class", "okved_group", "sector", "region", "revenue_minor", "loan", "leasing", "deposit", "fts_flag", "fts_country"} {
		allowed[name] = true
	}
	for i, name := range header {
		name = strings.TrimSpace(name)
		if !allowed[name] {
			return nil, nil, fmt.Errorf("unknown CSV column %q", name)
		}
		if _, ok := positions[name]; ok {
			return nil, nil, errors.New("duplicate CSV column")
		}
		positions[name] = i
	}
	for _, name := range []string{"id", "name", "okved_section", "okved_class", "okved_group", "sector", "region"} {
		if _, ok := positions[name]; !ok {
			return nil, nil, fmt.Errorf("missing CSV column %q", name)
		}
	}
	out := []model.Company{}
	seen := map[string]bool{}
	for line := 2; ; line++ {
		row, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, fmt.Errorf("CSV line %d: %w", line, err)
		}
		if len(out) >= 10000 {
			return nil, nil, errors.New("import batch exceeds 10000 companies")
		}
		get := func(key string) string {
			if i, ok := positions[key]; ok {
				return strings.TrimSpace(row[i])
			}
			return ""
		}
		c := model.Company{ID: get("id"), Name: get("name"), OKVED: model.OKVED{Section: get("okved_section"), Class: get("okved_class"), Group: get("okved_group")}, Sector: get("sector"), Region: get("region"), Products: map[string]*bool{}, VED: map[string]any{"country": get("fts_country")}}
		for _, key := range []string{"loan", "leasing", "deposit", "fts_flag"} {
			value := get(key)
			var flag *bool
			if value != "" {
				parsed, err := strconv.ParseBool(value)
				if err != nil {
					return nil, nil, fmt.Errorf("CSV line %d: invalid %s", line, key)
				}
				flag = &parsed
			}
			if key == "fts_flag" {
				c.VED[key] = flag
			} else {
				c.Products[key] = flag
			}
		}
		if v := get("revenue_minor"); v != "" {
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				return nil, nil, fmt.Errorf("CSV line %d: invalid revenue_minor", line)
			}
			c.RevenueMinor = &n
		}
		if err = ValidateCompany(c); err != nil {
			return nil, nil, fmt.Errorf("CSV line %d: %w", line, err)
		}
		if seen[c.ID] {
			return nil, nil, errors.New("duplicate company ID")
		}
		seen[c.ID] = true
		out = append(out, c)
	}
	var ps []model.Profile
	decoder := json.NewDecoder(bytes.NewReader(profileBytes))
	decoder.DisallowUnknownFields()
	if e = decoder.Decode(&ps); e != nil {
		return nil, nil, e
	}
	if e = decoder.Decode(new(any)); e != io.EOF {
		return nil, nil, errors.New("unexpected profile JSON tail")
	}
	seenProfiles := map[string]bool{}
	for _, p := range ps {
		if e = ValidateProfile(p); e != nil {
			return nil, nil, e
		}
		if !seen[p.CompanyID] || seenProfiles[p.CompanyID] {
			return nil, nil, errors.New("profile/company mismatch")
		}
		seenProfiles[p.CompanyID] = true
	}
	if len(ps) != len(out) || len(out) == 0 {
		return nil, nil, errors.New("one profile per company is required")
	}
	return out, ps, nil
}
func boundedImport(r io.Reader) ([]byte, error) {
	b, e := io.ReadAll(io.LimitReader(r, (10<<20)+1))
	if e != nil {
		return nil, e
	}
	if len(b) > 10<<20 {
		return nil, errors.New("import input exceeds 10 MiB")
	}
	return b, nil
}
