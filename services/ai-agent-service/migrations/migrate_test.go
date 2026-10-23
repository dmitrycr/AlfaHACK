package migrations

import (
	"alfa.local/shared/jobs"
	"alfa.local/shared/llm"
	"alfa.local/shared/platform"
	"alfa.local/shared/testdb"
	"context"
	"testing"
)

func TestUpgradePreservesLegacyCallsAndJobs(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Open(t, "agent")
	original := SQL + llm.Tables + jobs.Tables("agent")
	if e := platform.Migrate(ctx, pool, "agent", original); e != nil {
		t.Fatal(e)
	}
	_, e := pool.Exec(ctx, `INSERT INTO agent.calls(run_id,cache_key,scope,task,status,model,prompt_version,input_hash,reserved_minor,cost_minor,synthetic,lease_until) VALUES('legacy-run','legacy-cache','public','tag','ready','mock-v1','tag-backend-v1','legacy-hash',5,1,true,now())`)
	if e != nil {
		t.Fatal(e)
	}
	job, e := jobs.Create(ctx, pool, "agent", "chat", "legacy-company", "legacy-user", "legacy-key", true)
	if e != nil {
		t.Fatal(e)
	}
	if e = Apply(ctx, pool); e != nil {
		t.Fatal(e)
	}
	if e = Apply(ctx, pool); e != nil {
		t.Fatal(e)
	}
	var checksum string
	var cost, reserved int64
	if e = pool.QueryRow(ctx, "SELECT checksum FROM agent.schema_migrations WHERE version='001'").Scan(&checksum); e != nil || checksum != platform.Hash(original) {
		t.Fatal("v1 migration changed", e)
	}
	if e = pool.QueryRow(ctx, "SELECT cost_nano,reserved_nano FROM agent.calls WHERE run_id='legacy-run'").Scan(&cost, &reserved); e != nil || cost != llm.NanoPerMinor || reserved != 5*llm.NanoPerMinor {
		t.Fatal(cost, reserved, e)
	}
	got, e := (jobs.Store{Pool: pool, Schema: "agent"}).Get(ctx, job.ID, "legacy-company", "legacy-user")
	if e != nil || got.Status != "pending" {
		t.Fatal(got, e)
	}
}
