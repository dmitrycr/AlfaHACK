package platform_test

import (
	"context"
	"testing"

	"alfa.local/shared/platform"
	"alfa.local/shared/testdb"
)

func TestMigrationUpgradeKeepsExistingRowsAndChecksums(t *testing.T) {
	p := testdb.Open(t, "auth")
	ctx := context.Background()
	initial := `CREATE TABLE auth.upgrade_test(id text PRIMARY KEY); INSERT INTO auth.upgrade_test VALUES('existing');`
	if e := platform.Migrate(ctx, p, "auth", initial); e != nil {
		t.Fatal(e)
	}
	ms := []platform.Migration{{Version: "001", SQL: initial}, {Version: "002", SQL: `ALTER TABLE auth.upgrade_test ADD COLUMN title text NOT NULL DEFAULT '';`}}
	for i := 0; i < 2; i++ {
		if e := platform.MigrateAll(ctx, p, "auth", ms); e != nil {
			t.Fatal(e)
		}
	}
	var id, title string
	if e := p.QueryRow(ctx, `SELECT id,title FROM auth.upgrade_test`).Scan(&id, &title); e != nil || id != "existing" {
		t.Fatal("old data lost", e)
	}
	ms[0].SQL += "\n"
	if e := platform.MigrateAll(ctx, p, "auth", ms); e == nil {
		t.Fatal("changed applied migration accepted")
	}
}
