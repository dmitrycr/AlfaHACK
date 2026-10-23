package scoring

import (
	"alfa.local/shared/model"
	"encoding/json"
	"math"
	"reflect"
	"testing"
	"time"
)

func fixture() Request {
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	published := now.Add(-time.Hour)
	return Request{Company: model.Company{ID: "C1", Sector: "Steel", Region: "R", OKVED: model.OKVED{Class: "24"}},
		Profile: model.Profile{CompanyID: "C1", Version: 2, RulesetVersion: "profiles-v1", BuiltAt: now.Add(-24 * time.Hour), FactorExposure: map[string]*float64{}, Channels: []model.Channel{
			{Name: "industry", Weight: 1, Source: "data", Evidence: []string{"okved:24"}},
			{Name: "demand", Sector: "Construction", Weight: .5, Source: "data", Evidence: []string{"payments:aggregate"}},
		}}, AsOf: now, Events: []Event{{ID: "E1", Version: 3, Scope: "public", PublishedAt: &published, Tags: model.Tags{Type: "отраслевое", Sectors: []string{"Steel"}, Importance: 5, InputRevision: 3, ProviderVersion: "tags-v1"}}}}
}
func TestScoreExplainsEveryContribution(t *testing.T) {
	r := fixture()
	cfg := DefaultConfig()
	cfg.MinEvents = 1
	got, err := cfg.Rank(r)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(got.Items[0].Score-2.0/3) > 1e-12 {
		t.Fatal(got.Items[0].Score)
	}
	sum := 0.0
	for _, row := range got.Items[0].Breakdown {
		sum += row.Contribution
	}
	if math.Abs(sum-got.Items[0].Score) > 1e-12 {
		t.Fatal("breakdown does not reconcile")
	}
	if got.Items[0].ProfileVersion != 2 || got.Items[0].EventVersion != 3 || !got.Items[0].Passed {
		t.Fatal(got)
	}
	r.Profile.Channels = append(r.Profile.Channels, model.Channel{Name: "demand", Sector: "Other", Weight: .3, Source: "matrix", Evidence: []string{"matrix:v1"}})
	after, err := cfg.Rank(r)
	if err != nil {
		t.Fatal(err)
	}
	if after.Items[0].Score != got.Items[0].Score {
		t.Fatal("unrelated sector diluted score")
	}
}
func TestFreshnessBoundaries(t *testing.T) {
	cfg := DefaultConfig()
	for _, tc := range []struct {
		age  time.Duration
		want float64
	}{{24 * time.Hour, 1}, {24*time.Hour + 1, .8}, {72 * time.Hour, .8}, {72*time.Hour + 1, .5}, {7 * 24 * time.Hour, .5}, {7*24*time.Hour + 1, .3}, {14 * 24 * time.Hour, .3}, {14*24*time.Hour + 1, .15}} {
		if got := cfg.freshness(tc.age); got != tc.want {
			t.Fatalf("age %s got %g want %g", tc.age, got, tc.want)
		}
	}
}
func TestForeignStaleNoiseAndMissingDatesNeverRank(t *testing.T) {
	r := fixture()
	base := r.Events[0]
	r.Events = nil
	variants := []string{"foreign", "stale", "noise", "missing", "future"}
	for _, kind := range variants {
		e := base
		e.ID = kind
		switch kind {
		case "foreign":
			e.Scope = "company:other"
		case "stale":
			e.Tags.InputRevision = 1
		case "noise":
			e.IsNoise = true
			e.NoiseReason = "explicit fixture noise"
		case "missing":
			e.PublishedAt = nil
		case "future":
			future := r.AsOf.Add(time.Second)
			e.PublishedAt = &future
		}
		r.Events = append(r.Events, e)
	}
	got, err := DefaultConfig().Rank(r)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 0 || len(got.Excluded) != 5 || len(got.SelectedEventIDs) != 0 {
		t.Fatal(got)
	}
	r.Events[2].NoiseReason = ""
	if _, err = DefaultConfig().Rank(r); err == nil {
		t.Fatal("noise without reason")
	}
}
func TestFallbackStopsAtFloorAndNeverFillsWithZeroMatches(t *testing.T) {
	r := fixture()
	cfg := DefaultConfig()
	cfg.MinEvents = 1
	r.Events[0].Tags.Importance = 1 // 0.1333 < .15 but >= .10
	got, err := cfg.Rank(r)
	if err != nil {
		t.Fatal(err)
	}
	if got.Threshold != .10 || !got.ThresholdLowered || len(got.SelectedEventIDs) != 1 {
		t.Fatal(got)
	}
	r.Events[0].Tags.Sectors = []string{"Unrelated"}
	cfg.ThresholdMin = 0
	got, err = cfg.Rank(r)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.SelectedEventIDs) != 0 || got.Items[0].Passed || got.EmptyReason == "" {
		t.Fatal("zero match passed")
	}
}
func TestConfiguredAdjacencyOnlyOnFallback(t *testing.T) {
	r := fixture()
	r.Events[0].Tags.Sectors = []string{"Machinery"}
	cfg := DefaultConfig()
	cfg.MinEvents = 1
	cfg.AdjacentSectors = map[string][]string{"Steel": {"Machinery"}}
	got, err := cfg.Rank(r)
	if err != nil {
		t.Fatal(err)
	}
	if !got.AdjacentExpanded || !got.Items[0].Expanded || !got.Items[0].Passed {
		t.Fatal(got)
	}
	cfg.AdjacentSectors = map[string][]string{}
	none, err := cfg.Rank(r)
	if err != nil || len(none.SelectedEventIDs) != 0 {
		t.Fatal(none, err)
	}
}
func TestFactorUnknownZeroAndCommodityRequireEvidence(t *testing.T) {
	r := fixture()
	r.Profile.Channels = []model.Channel{{Name: "commodity", Weight: 1, Source: "matrix", Evidence: []string{"not parsed as facts"}}}
	r.Events[0].Tags.Factors = []string{"steel"}
	cfg := DefaultConfig()
	got, _ := cfg.Rank(r)
	if got.Items[0].Score != 0 {
		t.Fatal("guessed commodity exposure")
	}
	v := .6
	r.Profile.FactorExposure["steel"] = &v
	got, _ = cfg.Rank(r)
	if got.Items[0].Score != .6 {
		t.Fatal(got)
	}
	r.Profile.FactorExposure["steel"] = nil
	got, _ = cfg.Rank(r)
	if got.Items[0].Score != 0 {
		t.Fatal("unknown became exposed")
	}
	r.Profile.Channels[0].Name = "financial"
	r.Events[0].Tags.Factors = []string{"key_rate"}
	got, _ = cfg.Rank(r)
	if got.Items[0].Score != 1 {
		t.Fatal("coarse financial channel lost")
	}
	v = 0
	r.Profile.FactorExposure["key_rate"] = &v
	got, _ = cfg.Rank(r)
	if got.Items[0].Score != 0 {
		t.Fatal("explicit zero ignored")
	}
}
func TestStableOrderingAndInvalidConfig(t *testing.T) {
	r := fixture()
	second := r.Events[0]
	second.ID = "A"
	r.Events = append(r.Events, second)
	first, err := DefaultConfig().Rank(r)
	if err != nil {
		t.Fatal(err)
	}
	r.Events[0], r.Events[1] = r.Events[1], r.Events[0]
	secondResult, err := DefaultConfig().Rank(r)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, secondResult) || first.Items[0].EventID != "A" {
		t.Fatal("unstable rank")
	}
	bad := DefaultConfig()
	bad.ThresholdStep = 0
	if _, err = bad.Rank(r); err == nil {
		t.Fatal("non-terminating fallback accepted")
	}
	bad = DefaultConfig()
	bad.Freshness[0].Weight = math.NaN()
	if _, err = bad.Rank(r); err == nil {
		t.Fatal("nan config accepted")
	}
	r.Events[1].ID = "A"
	if _, err = DefaultConfig().Rank(r); err == nil {
		t.Fatal("duplicate event accepted")
	}
}
func TestJSONIsFinite(t *testing.T) {
	r := fixture()
	r.Profile.Channels = []model.Channel{}
	result, err := DefaultConfig().Rank(r)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = json.Marshal(result); err != nil {
		t.Fatal(err)
	}
	if result.Items[0].Score != 0 {
		t.Fatal("zero denominator")
	}
}
