package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

// buildSchemaV1 writes a database at the initial schema only, as a user who
// last ran an older devmodels would have, and fills it with rows in the shapes
// that schema used.
func buildSchemaV1(t *testing.T, path string) {
	t.Helper()
	ctx := context.Background()
	all, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()

	stmts := []string{
		`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, name TEXT NOT NULL, applied_at TEXT NOT NULL)`,
		all[0].sql,
		`INSERT INTO schema_migrations (version, name, applied_at) VALUES (1, '0001_initial.sql', '2026-01-01T00:00:00Z')`,
		`INSERT INTO sources (code, name, kind) VALUES ('epoch', 'Epoch AI', 'builtin')`,
		// A run under the old counting: five rows were "unresolved", three of
		// them only because their harness was unfamiliar.
		`INSERT INTO source_runs (id, source_code, started_at, status, published_rows, accepted_rows, unresolved_rows, quarantined_rows, excluded_rows, observation_count)
		 VALUES (1, 'epoch', '2026-01-01T00:00:00Z', 'success', 20, 15, 5, 3, 0, 15)`,
		`INSERT INTO unresolved_rows (source_code, run_id, source_model_name, source_row_ref, reason, quarantined, discovered_context)
		 VALUES ('epoch', 1, 'model-a', 'f.csv:2', 'score "n/a" is not a number', 0, '')`,
		`INSERT INTO unresolved_rows (source_code, run_id, source_model_name, source_row_ref, reason, quarantined, discovered_context)
		 VALUES ('epoch', 1, '(no model identifier)', 'f.csv:3', 'row has an empty Model version', 0, '')`,
		`INSERT INTO unresolved_rows (source_code, run_id, source_model_name, source_row_ref, reason, quarantined, discovered_context)
		 VALUES ('epoch', 1, 'model-b', 'f.csv:4', 'evaluation harness "Droid" is not recognised', 1, 'Droid')`,
		`INSERT INTO unresolved_rows (source_code, run_id, source_model_name, source_row_ref, reason, quarantined, discovered_context)
		 VALUES ('epoch', 1, 'model-c', 'f.csv:5', 'evaluation harness "Mux" is not recognised', 1, 'Mux')`,
		`INSERT INTO unresolved_rows (source_code, run_id, source_model_name, source_row_ref, reason, quarantined, discovered_context)
		 VALUES ('epoch', 1, 'model-d', 'f.csv:6', 'evaluation harness "Goose" is not recognised', 1, 'Goose')`,
		`INSERT INTO catalog_row_dispositions (row_ref, row_order, model_uid, label, disposition, reason, run_id)
		 VALUES ('modelCostData[0]', 0, 'a', 'A', 'accepted', '', 1)`,
		`INSERT INTO catalog_row_dispositions (row_ref, row_order, model_uid, label, disposition, reason, run_id)
		 VALUES ('modelCostData[1]', 1, 'b', 'B', 'excluded', 'hidden by the page', 1)`,
		`INSERT INTO catalog_row_dispositions (row_ref, row_order, model_uid, label, disposition, reason, run_id)
		 VALUES ('modelCostData[2]', 2, 'c', 'C', 'quarantined', 'model id c appears twice', 1)`,
	}
	for _, stmt := range stmts {
		if _, err := raw.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("building schema 1: %v\n%s", err, stmt)
		}
	}
}

func TestUpgradeFromSchemaV1ReclassifiesRows(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "old.db")
	buildSchemaV1(t, path)

	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if v, err := db.SchemaVersion(ctx); err != nil || v != MigrationCount() {
		t.Fatalf("schema version = %d (%v), want %d", v, err, MigrationCount())
	}

	// Rows that were only ever unfamiliar are not rejections, so they are not
	// carried over: the next refresh ingests them as evidence.
	rejected, err := ListRejected(ctx, db.SQL(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rejected) != 2 {
		t.Fatalf("carried over %d rejections, want the 2 that were rejections under both models: %+v", len(rejected), rejected)
	}
	for _, r := range rejected {
		if r.Source != "epoch" || r.Reason == "" || r.SourceRowRef == "" {
			t.Errorf("a carried-over rejection keeps its source, reason and row: %+v", r)
		}
	}

	// The stored counts move with the stored rows, so status stays consistent
	// until the next refresh replaces both.
	statuses, err := SourceStatuses(ctx, db.SQL())
	if err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 1 || statuses[0].RejectedRows != 2 {
		t.Fatalf("rejected count = %+v", statuses)
	}
	if run := statuses[0].LastSuccess; run == nil || run.Counts.Rejected != 2 || run.Counts.Published != 20 || run.Counts.Accepted != 15 {
		t.Fatalf("run counts = %+v", run)
	}

	// A dataset row held back for an identity collision was always a rejection
	// with an exact reason, so it keeps the reason and joins that state.
	disps, err := ListDispositions(ctx, db.SQL())
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, d := range disps {
		got[d.Disposition] = d.Reason
	}
	if _, ok := got["quarantined"]; ok {
		t.Error("no row may still be quarantined")
	}
	if got["rejected"] != "model id c appears twice" {
		t.Errorf("the collision keeps its reason: %+v", disps)
	}
	if len(disps) != 3 || got["accepted"] != "" {
		t.Errorf("every disposition survives the rebuild: %+v", disps)
	}

	// The old table is gone rather than left behind to drift.
	if _, err := db.SQL().ExecContext(ctx, `SELECT 1 FROM unresolved_rows`); err == nil {
		t.Error("unresolved_rows must not survive the migration")
	}
}
