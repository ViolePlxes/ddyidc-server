package admintodo

import (
	"context"
	"ddyidc/internal/shared/migrations"
	"ddyidc/internal/shared/testenv"
	"testing"
)

// Runs the queue query against the officially migrated schema instead of the
// hand-written fixtures, so a projection that references a column only the
// fixture defines fails here (that is how production broke on 2026-11-21).
func TestManualProductSchemaMySQL(t *testing.T) {
	db := testenv.MySQL(t)
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	steps, err := migrations.Steps()
	if err != nil {
		t.Fatal(err)
	}
	if err := migrations.Apply(ctx, pool, steps, false, nil); err != nil {
		t.Fatalf("apply official migrations: %v", err)
	}
	for _, kind := range []string{"manual-products"} {
		if _, err := List(ctx, db, kind, Filter{Page: 1, PageSize: 20}); err != nil {
			t.Fatalf("%s query failed on the migrated schema: %v", kind, err)
		}
		if _, err := List(ctx, db, kind, Filter{Page: 1, PageSize: 20, History: true}); err != nil {
			t.Fatalf("%s history query failed on the migrated schema: %v", kind, err)
		}
	}
	if summary := Read(ctx, db); len(summary.Items) == 0 {
		t.Fatal("todo summary returned no items on the migrated schema")
	}
}
