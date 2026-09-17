package app_test

import (
	"strings"
	"testing"

	"github.com/Tim-Butterfield/devin-model-catalog/internal/app"
)

// A context's kind decides whether its values are proxies, so an import may
// reuse a built-in context but must not redefine it.
func TestImportCannotRedefineAnExistingContext(t *testing.T) {
	h := newHarness(t)
	doc := func(kind string) []byte {
		return []byte(`{
		  "source": {"code": "manual_harness", "name": "Harness notes", "access_basis": "typed in"},
		  "evaluation_contexts": [{"code": "claude_code", "name": "Claude Code", "kind": "` + kind + `"}],
		  "observations": [{"metric": "swe_bench_verified", "model": "Claude Opus 5", "evaluation_context": "claude_code", "value": 70}]
		}`)
	}
	kindOf := func() int {
		return h.count(`SELECT COUNT(*) FROM evaluation_contexts WHERE code = 'claude_code' AND kind = 'external_harness'`)
	}

	_, err := h.svc.Import(h.ctx, doc("devin"))
	if app.KindOf(err) != app.KindUsage || !strings.Contains(err.Error(), "claude_code") {
		t.Fatalf("redefining a context's kind must be a usage error: %v", err)
	}
	if kindOf() != 1 || h.count(`SELECT COUNT(*) FROM sources WHERE code = 'manual_harness'`) != 0 {
		t.Fatal("a rejected import must change nothing")
	}

	if _, err := h.svc.Import(h.ctx, doc("external_harness")); err != nil {
		t.Fatalf("reusing a context with its own kind is allowed: %v", err)
	}
	if kindOf() != 1 {
		t.Fatal("reusing a context must not change it")
	}
}
