package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tim-Butterfield/devin-model-catalog/internal/devin"
)

func TestMigrationsAreIdempotent(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "nested", "devmodels.db")
	for i := 0; i < 3; i++ {
		db, err := Open(ctx, path)
		if err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
		v, err := db.SchemaVersion(ctx)
		if err != nil || v != MigrationCount() {
			t.Fatalf("schema version %d (want %d): %v", v, MigrationCount(), err)
		}
		var rows int
		if err := db.SQL().QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&rows); err != nil || rows != MigrationCount() {
			t.Fatalf("migration ledger has %d rows: %v", rows, err)
		}
		var fk int
		if err := db.SQL().QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&fk); err != nil || fk != 1 {
			t.Fatal("foreign keys must be enforced")
		}
		db.Close()
	}
}

func TestSchemaHasNoBenchmarkColumns(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.SQL().QueryContext(ctx, `SELECT m.name, p.name FROM sqlite_master m JOIN pragma_table_info(m.name) p WHERE m.type = 'table'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns := 0
	for rows.Next() {
		var table, column string
		if err := rows.Scan(&table, &column); err != nil {
			t.Fatal(err)
		}
		columns++
		for _, banned := range []string{"swe_bench", "bfcl", "terminal", "deepswe", "frontiercode", "context_window"} {
			if strings.Contains(column, banned) {
				t.Errorf("%s.%s is a metric-specific column; metrics must be rows", table, column)
			}
		}
	}
	if err := rows.Err(); err != nil || columns == 0 {
		t.Fatalf("schema inspection read %d columns: %v", columns, err)
	}
}

func TestInventoryIDsRestartAtOne(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mk := func(keys ...string) []devin.Dataset {
		var out []devin.Dataset
		for i, k := range keys {
			out = append(out, devin.Dataset{Position: i + 1, SourceKey: k, DisplayName: k, Component: "ModelCosts", Supported: true})
		}
		return out
	}
	for _, keys := range [][]string{{"a", "b", "c"}, {"c", "a"}, {"d", "e", "f", "g"}} {
		if _, err := ReplaceDatasetInventory(ctx, db.SQL(), mk(keys...), "u", "sha", nil, time.Now()); err != nil {
			t.Fatal(err)
		}
		rows, inv, err := ListDatasets(ctx, db.SQL())
		if err != nil {
			t.Fatal(err)
		}
		if inv.Count != len(keys) || len(rows) != len(keys) {
			t.Fatalf("inventory count %d rows %d want %d", inv.Count, len(rows), len(keys))
		}
		for i, r := range rows {
			if r.ID != i+1 || r.SourceKey != keys[i] {
				t.Fatalf("row %d = id %d key %s", i, r.ID, r.SourceKey)
			}
		}
	}
}
