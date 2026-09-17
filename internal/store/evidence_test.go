package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tim-Butterfield/devin-model-catalog/internal/metrics"
	"github.com/Tim-Butterfield/devin-model-catalog/internal/sources"
)

func evidenceDB(t *testing.T) *DB {
	t.Helper()
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "evidence.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := UpsertSource(ctx, db.SQL(), sources.Info{Code: "src", Name: "Source", Kind: "builtin"}); err != nil {
		t.Fatal(err)
	}
	if err := UpsertMetric(ctx, db.SQL(), metrics.Definition{
		Key: "score", DisplayName: "Score", Description: "A score.", ValueKind: metrics.KindNumber,
		Direction: metrics.HigherIsBetter, Scope: metrics.ScopeEvidence, DefinedBy: "src", Status: metrics.StatusCurrent,
	}); err != nil {
		t.Fatal(err)
	}
	return db
}

func runWith(t *testing.T, db *DB, res sources.Result) error {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	return db.Tx(ctx, func(tx *sql.Tx) error {
		runID, err := StartRun(ctx, tx, "src", now)
		if err != nil {
			return err
		}
		return ReplaceEvidence(ctx, tx, "src", runID, res, now)
	})
}

// A collector may introduce a context, because that is how a new upstream
// harness reaches the catalog without a release.
func TestReplaceEvidenceRecordsRunDiscoveredContexts(t *testing.T) {
	db := evidenceDB(t)
	discovered := sources.EvaluationContext{Code: "harness_droid", Name: "Droid", Kind: sources.ContextExternalHarness}
	res := sources.Result{
		SourceRowCount: 1, AcceptedRows: 1,
		Contexts: []sources.EvaluationContext{discovered},
		Observations: []sources.Observation{{
			Metric: "score", SourceModelName: "m", SourceRowRef: "f:1", BaseKey: "m",
			EvaluationContext: discovered.Code, Value: sources.NumberValue(1),
		}},
	}
	if err := runWith(t, db, res); err != nil {
		t.Fatal(err)
	}
	ctxs, err := ListContexts(context.Background(), db.SQL())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range ctxs {
		if c == discovered {
			found = true
		}
	}
	if !found {
		t.Fatalf("the derived context must be recorded before the observations citing it: %+v", ctxs)
	}
}

// It may not reclassify one. A context's kind decides whether its values are
// proxies, so changing it would silently change what every stored observation
// citing it means; the refresh fails instead and the previous data stands.
func TestReplaceEvidenceRefusesToChangeAContextKind(t *testing.T) {
	db := evidenceDB(t)
	first := sources.EvaluationContext{Code: "harness_droid", Name: "Droid", Kind: sources.ContextExternalHarness}
	res := sources.Result{
		SourceRowCount: 1, AcceptedRows: 1,
		Contexts: []sources.EvaluationContext{first},
		Observations: []sources.Observation{{
			Metric: "score", SourceModelName: "m", SourceRowRef: "f:1", BaseKey: "m",
			EvaluationContext: first.Code, Value: sources.NumberValue(1),
		}},
	}
	if err := runWith(t, db, res); err != nil {
		t.Fatal(err)
	}

	res.Contexts[0].Kind = sources.ContextDevin
	err := runWith(t, db, res)
	if err == nil {
		t.Fatal("reclassifying a context must fail the refresh")
	}
	for _, want := range []string{"harness_droid", "cannot change kind", "proxy"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error must explain what changed and why it matters; got %v", err)
		}
	}
	// The failed run left the previous classification and evidence in place.
	kind, err := ContextKind(context.Background(), db.SQL(), first.Code)
	if err != nil || kind != sources.ContextExternalHarness {
		t.Fatalf("kind = %q (%v), want the original", kind, err)
	}
}

// A kind outside the closed set this project owns is refused outright, so a
// collector cannot smuggle one in through the result.
func TestReplaceEvidenceRefusesAnUnknownContextKind(t *testing.T) {
	db := evidenceDB(t)
	err := runWith(t, db, sources.Result{
		Contexts: []sources.EvaluationContext{{Code: "x", Name: "X", Kind: sources.ContextKind("invented")}},
	})
	if err == nil || !strings.Contains(err.Error(), "invented") {
		t.Fatalf("an unknown context kind must be refused, got %v", err)
	}
}
