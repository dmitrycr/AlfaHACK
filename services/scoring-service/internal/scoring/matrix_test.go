package scoring

import (
	"encoding/json"
	"math"
	"os"
	"strings"
	"testing"
)

func TestDirectionalMatrixFallback(t *testing.T) {
	r := fixture()
	r.Events[0].Tags.Sectors = []string{"Machinery"}
	cfg := DefaultConfig()
	cfg.MinEvents = 1
	cfg.SectorRelations = []SectorRelation{{FromSector: "Steel", ToSector: "Machinery", Role: "demand", Strength: .8, Rationale: "buys steel"}}
	got, err := cfg.Rank(r)
	if err != nil || !got.Items[0].Passed || !got.Items[0].Expanded || math.Abs(got.Items[0].Score-1.0/15) > 1e-12 {
		t.Fatalf("%+v %v", got, err)
	}
	found := false
	for _, c := range got.Items[0].Breakdown {
		if strings.HasPrefix(c.Reason, "sector_matrix:demand:Machinery:") {
			found = true
		}
	}
	if !found {
		t.Fatal("missing matrix provenance")
	}
	// Exact demand match retains its original score even during fallback.
	cfg.MinEvents = 5
	cfg.SectorRelations[0].ToSector = "Construction"
	r.Events[0].Tags.Sectors = []string{"Construction"}
	exact, err := cfg.Rank(r)
	if err != nil || exact.Items[0].Expanded || math.Abs(exact.Items[0].Score-1.0/3) > 1e-12 {
		t.Fatalf("exact match changed: %+v %v", exact, err)
	}
	// A supply edge must not activate a demand channel; reverse links aren't inferred.
	r.Events[0].Tags.Sectors = []string{"Machinery"}
	cfg.SectorRelations[0] = SectorRelation{FromSector: "Steel", ToSector: "Machinery", Role: "supply", Strength: .8, Rationale: "sells equipment"}
	wrongRole, _ := cfg.Rank(r)
	if wrongRole.Items[0].Score != 0 {
		t.Fatal("supply leaked into demand")
	}
	cfg.SectorRelations[0] = SectorRelation{FromSector: "Machinery", ToSector: "Steel", Role: "demand", Strength: .8, Rationale: "buys steel"}
	reversed, _ := cfg.Rank(r)
	if reversed.Items[0].Score != 0 {
		t.Fatal("reverse edge inferred")
	}
	// With enough exact news, adjacency never enters scoring.
	cfg.MinEvents = 1
	cfg.SectorRelations[0] = SectorRelation{FromSector: "Steel", ToSector: "Machinery", Role: "demand", Strength: .8, Rationale: "buys steel"}
	e := r.Events[0]
	e.ID = "Exact"
	e.Tags.Sectors = []string{"Steel"}
	r.Events = append(r.Events, e)
	sufficient, _ := cfg.Rank(r)
	if sufficient.AdjacentExpanded || len(sufficient.SelectedEventIDs) != 1 || sufficient.SelectedEventIDs[0] != "Exact" {
		t.Fatal(sufficient)
	}
}

func TestReviewedConfigAndInvalidRelations(t *testing.T) {
	data, err := os.ReadFile("../../config/scoring.json")
	if err != nil {
		t.Fatal(err)
	}
	var cfg Config
	if err = json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	if err = cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if len(cfg.SectorRelations) != 74 || cfg.Version != "f2-sector-matrix-v1" {
		t.Fatal("unexpected reviewed matrix")
	}
	baseline := cfg.SectorRelations[0]
	for _, bad := range []SectorRelation{
		{FromSector: "A", ToSector: "A", Role: "demand", Strength: .5, Rationale: "self"},
		{FromSector: "A", ToSector: "B", Role: "industry", Strength: .5, Rationale: "wrong role"},
		{FromSector: "A", ToSector: "B", Role: "demand", Strength: math.NaN(), Rationale: "bad weight"},
		{FromSector: "A", ToSector: "B", Role: "supply", Strength: .5},
		baseline,
	} {
		copyCfg := cfg
		copyCfg.SectorRelations = append(append([]SectorRelation{}, cfg.SectorRelations...), bad)
		if copyCfg.Validate() == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
	// A real sector name from the pinned ML dictionary activates a reviewed edge.
	r := fixture()
	r.Company.Sector = "Металлургия и добыча"
	r.Events[0].Tags.Sectors = []string{"Машиностроение и промышленность"}
	got, err := cfg.Rank(r)
	if err != nil || !got.Items[0].Expanded || !got.Items[0].Passed {
		t.Fatalf("%+v %v", got, err)
	}
}
