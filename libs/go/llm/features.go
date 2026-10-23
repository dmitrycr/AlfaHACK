package llm

import (
	"alfa.local/shared/model"
	"fmt"
	"sort"
)

// FeaturesFor applies the same rules to every imported company. No hand-written
// company scenarios and no conversion of payment turnover into revenue.
func FeaturesFor(company model.Company, profile model.Profile) []Feature {
	out := []Feature{}
	if company.Sector != "" {
		out = append(out, Feature{ID: "company.sector", Text: "Отрасль компании: " + company.Sector})
	}
	if company.Region != "" {
		out = append(out, Feature{ID: "company.region", Text: "Регион компании: " + company.Region})
	}
	for i, c := range profile.Channels {
		if len(out) >= 30 {
			break
		}
		if len(c.Evidence) > 0 {
			out = append(out, Feature{ID: fmt.Sprintf("profile.channels.%d", i), Text: fmt.Sprintf("Канал %s; сектор %s; вес %.3f; основание %s: %s", c.Name, c.Sector, c.Weight, c.Source, c.Evidence[0])})
		}
	}
	keys := []string{}
	for k, v := range company.Products {
		if v != nil && *v {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		if len(out) >= 40 {
			break
		}
		out = append(out, Feature{ID: "company.products." + k, Text: "Используется банковский продукт: " + k})
	}
	return out
}
