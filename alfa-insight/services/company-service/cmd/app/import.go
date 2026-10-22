package main

import (
	"alfa.local/company-service/internal/service"
	"alfa.local/shared/model"
	"encoding/json"
	"flag"
	"log/slog"
	"os"
)

func readImport() (bool, []model.Company, []model.Profile, error) {
	flags := flag.NewFlagSet("import", flag.ContinueOnError)
	csvPath := flags.String("companies", "", "normalized CSV file")
	profilesPath := flags.String("profiles", "", "profile JSON array")
	dry := flags.Bool("dry-run", false, "validate without database access")
	if e := flags.Parse(os.Args[2:]); e != nil {
		return false, nil, nil, e
	}
	csv, e := os.Open(*csvPath)
	if e != nil {
		return false, nil, nil, e
	}
	defer csv.Close()
	profiles, e := os.Open(*profilesPath)
	if e != nil {
		return false, nil, nil, e
	}
	defer profiles.Close()
	companies, ps, e := service.ReadImport(csv, profiles)
	if e != nil {
		return false, nil, nil, e
	}
	if *dry {
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"dry_run": true, "companies": len(companies), "profiles": len(ps)})
		slog.Info("import validation completed")
	}
	return *dry, companies, ps, nil
}
