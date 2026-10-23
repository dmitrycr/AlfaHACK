// Package scoring implements a deterministic, versioned F2 baseline. No LLM calls.
package scoring

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"alfa.local/shared/model"
)

type Step struct {
	Days   float64 `json:"max_age_days"`
	Weight float64 `json:"weight"`
}
type Config struct {
	Version         string              `json:"ruleset_version"`
	Threshold       float64             `json:"threshold"`
	ThresholdMin    float64             `json:"threshold_min"`
	ThresholdStep   float64             `json:"threshold_step"`
	MinEvents       int                 `json:"min_events"`
	Freshness       []Step              `json:"freshness_steps"`
	AdjacentWeight  float64             `json:"adjacent_weight"`
	AdjacentSectors map[string][]string `json:"adjacent_sectors"`
}

func DefaultConfig() Config {
	return Config{Version: "f2-baseline-v1", Threshold: .15, ThresholdMin: .05, ThresholdStep: .05, MinEvents: 5,
		Freshness: []Step{{1, 1}, {3, .8}, {7, .5}, {14, .3}, {9999, .15}}, AdjacentWeight: .25, AdjacentSectors: map[string][]string{}}
}
func unit(x float64) bool { return !math.IsNaN(x) && !math.IsInf(x, 0) && x >= 0 && x <= 1 }
func (c Config) Validate() error {
	if strings.TrimSpace(c.Version) == "" || !unit(c.Threshold) || !unit(c.ThresholdMin) || c.ThresholdMin > c.Threshold || !unit(c.ThresholdStep) || c.ThresholdStep <= 0 || c.ThresholdStep < .001 || c.MinEvents < 1 || c.MinEvents > 100 || !unit(c.AdjacentWeight) || len(c.Freshness) == 0 {
		return errors.New("invalid scoring configuration")
	}
	prev, weight := 0.0, 1.0
	for _, s := range c.Freshness {
		if math.IsNaN(s.Days) || math.IsInf(s.Days, 0) || s.Days <= prev || !unit(s.Weight) || s.Weight > weight {
			return errors.New("invalid freshness steps")
		}
		prev, weight = s.Days, s.Weight
	}
	for sector, adjacent := range c.AdjacentSectors {
		if strings.TrimSpace(sector) == "" {
			return errors.New("empty adjacent sector")
		}
		for _, a := range adjacent {
			if strings.TrimSpace(a) == "" {
				return errors.New("empty adjacent sector")
			}
		}
	}
	return nil
}

type Event struct {
	ID          string     `json:"id"`
	Version     int        `json:"version"`
	Scope       string     `json:"scope"`
	PublishedAt *time.Time `json:"published_at"`
	Tags        model.Tags `json:"tags"`
	Regions     []string   `json:"regions,omitempty"`
	IsNoise     bool       `json:"is_noise,omitempty"`
	NoiseReason string     `json:"noise_reason,omitempty"`
}
type Request struct {
	Company model.Company `json:"company"`
	Profile model.Profile `json:"profile"`
	Events  []Event       `json:"events"`
	AsOf    time.Time     `json:"as_of"`
}
type Contribution struct {
	Channel      string   `json:"channel"`
	Sector       string   `json:"sector,omitempty"`
	Source       string   `json:"source"`
	Evidence     []string `json:"evidence"`
	Weight       float64  `json:"weight"`
	Match        float64  `json:"match"`
	Contribution float64  `json:"contribution"`
	Reason       string   `json:"reason"`
}
type Item struct {
	CompanyID      string         `json:"company_id"`
	EventID        string         `json:"event_id"`
	ProfileVersion int            `json:"profile_version"`
	EventVersion   int            `json:"event_version"`
	RulesetVersion string         `json:"ruleset_version"`
	Score          float64        `json:"score"`
	Freshness      float64        `json:"freshness"`
	Importance     float64        `json:"importance"`
	Breakdown      []Contribution `json:"breakdown"`
	Passed         bool           `json:"passed_level"`
	Reason         string         `json:"reason"`
	Expanded       bool           `json:"expanded"`
}
type Result struct {
	CompanyID        string         `json:"company_id"`
	ProfileVersion   int            `json:"profile_version"`
	RulesetVersion   string         `json:"ruleset_version"`
	AsOf             time.Time      `json:"as_of"`
	Threshold        float64        `json:"threshold"`
	ThresholdLowered bool           `json:"threshold_lowered"`
	AdjacentExpanded bool           `json:"adjacent_expanded"`
	Items            []Item         `json:"items"`
	SelectedEventIDs []string       `json:"selected_event_ids"`
	Excluded         map[string]int `json:"excluded"`
	Warnings         []string       `json:"warnings"`
	EmptyReason      string         `json:"empty_reason,omitempty"`
}

var channelNames = map[string]bool{"industry": true, "competitor": true, "demand": true, "supply": true, "financial": true, "currency": true, "commodity": true, "regional": true, "regulatory": true}

func (r Request) Validate() error {
	if r.Company.ID == "" || r.Company.ID != r.Profile.CompanyID || r.Profile.Version < 1 || r.Profile.RulesetVersion == "" || r.Profile.BuiltAt.IsZero() || r.AsOf.IsZero() || r.Profile.Channels == nil || r.Profile.FactorExposure == nil || r.Events == nil || len(r.Events) > 500 {
		return errors.New("invalid ranking snapshot")
	}
	if r.Profile.BuiltAt.After(r.AsOf) {
		return errors.New("profile built after scoring time")
	}
	seen := map[string]bool{}
	for _, ch := range r.Profile.Channels {
		key := ch.Name + "\x00" + ch.Sector
		if seen[key] || !channelNames[ch.Name] || !unit(ch.Weight) || (ch.Source != "data" && ch.Source != "matrix" && ch.Source != "llm") || len(ch.Evidence) == 0 {
			return errors.New("invalid profile channel")
		}
		seen[key] = true
	}
	for _, value := range r.Profile.FactorExposure {
		if value != nil && !unit(*value) {
			return errors.New("invalid factor exposure")
		}
	}
	seen = map[string]bool{}
	for _, e := range r.Events {
		if strings.TrimSpace(e.ID) == "" || seen[e.ID] || e.Version < 1 || e.Tags.InputRevision < 1 || e.Tags.ProviderVersion == "" || e.Tags.Importance < 1 || e.Tags.Importance > 5 || e.Tags.Type == "" || (e.Scope != "public" && !strings.HasPrefix(e.Scope, "company:")) || e.Scope == "company:" {
			return errors.New("invalid or duplicate event")
		}
		seen[e.ID] = true
		if e.IsNoise && strings.TrimSpace(e.NoiseReason) == "" {
			return errors.New("noise requires a reason")
		}
	}
	return nil
}
func normalized(s string) string { return strings.ToLower(strings.TrimSpace(s)) }
func contains(values []string, want string) bool {
	if normalized(want) == "" {
		return false
	}
	for _, v := range values {
		if normalized(v) == normalized(want) {
			return true
		}
	}
	return false
}
func (c Config) freshness(age time.Duration) float64 {
	days := age.Hours() / 24
	for _, s := range c.Freshness {
		if days <= s.Days {
			return s.Weight
		}
	}
	return c.Freshness[len(c.Freshness)-1].Weight
}
func (c Config) sectorMatch(sector string, sectors []string, expand bool) (float64, string) {
	if contains(sectors, sector) {
		return 1, "sector_exact"
	}
	if expand {
		for base, adjacent := range c.AdjacentSectors {
			if normalized(base) == normalized(sector) {
				for _, a := range adjacent {
					if contains(sectors, a) {
						return c.AdjacentWeight, "adjacent_sector"
					}
				}
			}
		}
	}
	return 0, "no_sector_match"
}
func (c Config) match(ch model.Channel, r Request, e Event, expand bool) (float64, string) {
	switch ch.Name {
	case "industry", "competitor":
		value, reason := c.sectorMatch(r.Company.Sector, e.Tags.Sectors, expand)
		if contains(e.Tags.OKVEDClasses, r.Company.OKVED.Class) {
			return 1, "okved_class_exact"
		}
		return value, reason
	case "demand", "supply":
		return c.sectorMatch(ch.Sector, e.Tags.Sectors, expand)
	case "regional":
		if contains(e.Regions, r.Company.Region) {
			return 1, "region_exact"
		}
	case "regulatory":
		if contains([]string{"регулирование", "санкции", "regulation", "sanctions"}, e.Tags.Type) {
			value, reason := c.sectorMatch(r.Company.Sector, e.Tags.Sectors, expand)
			if contains(e.Tags.OKVEDClasses, r.Company.OKVED.Class) {
				return 1, "regulatory_okved"
			}
			return value, reason
		}
	case "financial", "currency", "commodity":
		categories := map[string][]string{"financial": {"key_rate", "interest_rate", "inflation"}, "currency": {"usd", "cny", "eur", "usd_try"}, "commodity": {"oil", "gas", "steel", "coal", "fertilizers", "grain", "gold"}}
		best := 0.0
		reason := "no_factor_match"
		for _, factor := range e.Tags.Factors {
			if !contains(categories[ch.Name], factor) {
				continue
			}
			value, found := exposure(r.Profile.FactorExposure, factor)
			// Financial/currency channels already encode coarse rule-based exposure.
			// A commodity requires an explicit company factor; never guess it from evidence prose.
			if !found && ch.Name != "commodity" {
				value = 1
			}
			if value > best {
				best = value
				reason = "factor:" + normalized(factor)
			}
		}
		return best, reason
	}
	return 0, "no_match"
}
func exposure(values map[string]*float64, factor string) (float64, bool) {
	// Prefer the exact key, then a deterministic canonical alias; null means unknown.
	if v, ok := values[factor]; ok {
		if v == nil {
			return 0, true
		}
		return *v, true
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if normalized(key) == normalized(factor) {
			if values[key] == nil {
				return 0, true
			}
			return *values[key], true
		}
	}
	return 0, false
}
func (c Config) score(r Request, e Event, expand bool) Item {
	item := Item{CompanyID: r.Company.ID, EventID: e.ID, ProfileVersion: r.Profile.Version, EventVersion: e.Version, RulesetVersion: c.Version, Freshness: c.freshness(r.AsOf.Sub(*e.PublishedAt)), Importance: float64(e.Tags.Importance) / 5, Breakdown: []Contribution{}, Reason: "no_matching_channels"}
	maxima := map[string]float64{}
	chosen := map[string]Contribution{}
	for _, ch := range r.Profile.Channels {
		if ch.Weight > maxima[ch.Name] {
			maxima[ch.Name] = ch.Weight
		}
		match, reason := c.match(ch, r, e, expand)
		row := Contribution{Channel: ch.Name, Sector: ch.Sector, Source: ch.Source, Evidence: ch.Evidence, Weight: ch.Weight, Match: match, Reason: reason}
		prior, ok := chosen[ch.Name]
		if !ok || row.Weight*row.Match > prior.Weight*prior.Match || (row.Weight*row.Match == prior.Weight*prior.Match && row.Sector < prior.Sector) {
			chosen[ch.Name] = row
		}
	}
	keys := make([]string, 0, len(chosen))
	for key := range chosen {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	denominator := 0.0
	for _, key := range keys {
		denominator += maxima[key]
	}
	matches := []string{}
	for _, key := range keys {
		row := chosen[key]
		if denominator > 0 {
			row.Contribution = row.Weight * row.Match / denominator * item.Freshness * item.Importance
		}
		item.Score += row.Contribution
		if row.Contribution > 0 {
			matches = append(matches, key)
			if row.Reason == "adjacent_sector" {
				item.Expanded = true
			}
		}
		item.Breakdown = append(item.Breakdown, row)
	}
	item.Score = math.Min(1, math.Max(0, item.Score))
	if len(matches) > 0 {
		item.Reason = "matched_channels:" + strings.Join(matches, ",")
	}
	return item
}
func (c Config) Rank(r Request) (Result, error) {
	if err := c.Validate(); err != nil {
		return Result{}, err
	}
	if err := r.Validate(); err != nil {
		return Result{}, err
	}
	out := Result{CompanyID: r.Company.ID, ProfileVersion: r.Profile.Version, RulesetVersion: c.Version, AsOf: r.AsOf, Threshold: c.Threshold, Items: []Item{}, SelectedEventIDs: []string{}, Excluded: map[string]int{}, Warnings: []string{"baseline_not_calibrated_on_golden_set"}}
	valid := []Event{}
	for _, e := range r.Events {
		reason := ""
		switch {
		case e.Scope != "public" && e.Scope != "company:"+r.Company.ID:
			reason = "foreign_scope"
		case e.Tags.InputRevision != e.Version:
			reason = "stale_tags"
		case e.IsNoise:
			reason = "explicit_noise"
		case e.PublishedAt == nil || e.PublishedAt.IsZero():
			reason = "missing_publication_time"
		case e.PublishedAt.After(r.AsOf):
			reason = "future_publication"
		}
		if reason != "" {
			out.Excluded[reason]++
			continue
		}
		valid = append(valid, e)
		out.Items = append(out.Items, c.score(r, e, false))
	}
	selected := func() int {
		n := 0
		for _, item := range out.Items {
			if item.Score > 0 && item.Score+1e-12 >= out.Threshold {
				n++
			}
		}
		return n
	}
	for selected() < c.MinEvents && out.Threshold > c.ThresholdMin+1e-12 {
		out.Threshold = math.Max(c.ThresholdMin, math.Round((out.Threshold-c.ThresholdStep)*1e9)/1e9)
	}
	out.ThresholdLowered = out.Threshold < c.Threshold
	if selected() < c.MinEvents && len(c.AdjacentSectors) > 0 {
		out.AdjacentExpanded = true
		for i, e := range valid {
			out.Items[i] = c.score(r, e, true)
		}
	}
	sort.Slice(out.Items, func(i, j int) bool {
		if out.Items[i].Score != out.Items[j].Score {
			return out.Items[i].Score > out.Items[j].Score
		}
		return out.Items[i].EventID < out.Items[j].EventID
	})
	for i := range out.Items {
		item := &out.Items[i]
		item.Passed = item.Score > 0 && item.Score+1e-12 >= out.Threshold
		if item.Passed {
			out.SelectedEventIDs = append(out.SelectedEventIDs, item.EventID)
		}
	}
	if len(out.SelectedEventIDs) < c.MinEvents {
		out.Warnings = append(out.Warnings, "fewer_than_min_events")
		if len(c.AdjacentSectors) == 0 {
			out.Warnings = append(out.Warnings, "adjacent_sector_matrix_not_configured")
		}
	}
	if len(out.SelectedEventIDs) == 0 {
		out.EmptyReason = fmt.Sprintf("no_positive_matches_at_threshold_%.3f", out.Threshold)
	}
	return out, nil
}
