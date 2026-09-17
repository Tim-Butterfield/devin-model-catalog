package cli

import (
	"math"
	"testing"
)

func TestParseCriterion(t *testing.T) {
	cases := []struct {
		expr  string
		op    string
		value any
	}{
		{"score>-5", "gt", -5.0},
		{"score<=0.5", "lte", 0.5},
		{"score == 5", "eq", 5.0},
		{"score!=5", "ne", 5.0},
		{"tokens>=200K", "gte", 200_000.0},
		{"tokens<1.5m", "lt", 1_500_000.0},
		{"flag=true", "eq", true},
		{"tier=medium", "eq", "medium"},
		{"mode=fork", "eq", "fork"},
		{"tier='5k'", "eq", "5k"},
		{`tier="true"`, "eq", "true"},
		{"score", "present", nil},
	}
	for _, c := range cases {
		got, err := parseCriterion(c.expr, "reject")
		if err != nil {
			t.Errorf("%s: %v", c.expr, err)
			continue
		}
		if got.Op != c.op || got.Value != c.value {
			t.Errorf("%s = %s %#v, want %s %#v", c.expr, got.Op, got.Value, c.op, c.value)
		}
	}

	// NaN parses as a number; the query engine rejects it as non-finite.
	if got, err := parseCriterion("score>=nan", "reject"); err != nil || !math.IsNaN(got.Value.(float64)) {
		t.Errorf("nan: %#v %v", got.Value, err)
	}
	for _, bad := range []string{"score=>5", "score>>1", "Score>1", ">5"} {
		if _, err := parseCriterion(bad, "reject"); err == nil {
			t.Errorf("%s must be rejected", bad)
		}
	}
	if _, err := parseCriterion("score", "allow"); err == nil {
		t.Error("a bare metric cannot allow missing")
	}
}
