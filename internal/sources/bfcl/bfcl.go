// Package bfcl collects the Berkeley Function-Calling Leaderboard.
//
// BFCL evaluates models directly, without an agent harness, in named modes:
// native function calling ("FC") or prompted tool use ("Prompt"), with or
// without thinking. The mode changes the score, so it is the evaluation
// context.
//
// A mode this build does not curate is carried as its own distinct context
// rather than discarded, so the leaderboard adding a mode needs no devmodels
// release. What such a value never becomes is one of the curated modes: two
// modes are the same context only when their labels fold to the same thing.
package bfcl

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"strconv"
	"strings"

	"github.com/Tim-Butterfield/devin-model-catalog/internal/identity"
	"github.com/Tim-Butterfield/devin-model-catalog/internal/metrics"
	"github.com/Tim-Butterfield/devin-model-catalog/internal/sources"
)

// SourceCode identifies this source.
const SourceCode = "bfcl"

// DataURL is the leaderboard's published overall-results file.
const DataURL = "https://raw.githubusercontent.com/ShishirPatil/gorilla/gh-pages/data_overall.csv"

// Metric is the metric this collector emits.
const Metric = "bfcl_overall_accuracy"

// derivedModePrefix namespaces the code of a mode this build does not curate,
// so a derived code can never collide with one of the curated mode codes.
const derivedModePrefix = "bfcl_mode_"

// UnstatedModeContext is the context of a row whose model name carries no mode
// qualifier. BFCL is always a direct evaluation, so that much is known; which
// mode produced the number is not, and saying so is the faithful reading. It
// is deliberately its own context, never merged with a curated mode.
//
// A leaderboard mode literally spelled "unstated" would fold into this same
// context, which is the reading that entry would deserve anyway.
const UnstatedModeContext = derivedModePrefix + "unstated"

// modes maps a published mode qualifier, in its FoldContextLabel form, to a
// curated context. The keys are folds, so "FC thinking", "fc-thinking" and
// "FC Thinking" need no separate entries, and "Prompt + Thinking" folds to
// prompt_thinking. Only the stable published codes are listed here.
var modes = map[string]sources.EvaluationContext{
	"fc":              {Code: "bfcl_fc", Name: "BFCL native function calling", Kind: sources.ContextDirect},
	"prompt":          {Code: "bfcl_prompt", Name: "BFCL prompted tool use", Kind: sources.ContextDirect},
	"fc_thinking":     {Code: "bfcl_fc_thinking", Name: "BFCL native function calling with thinking", Kind: sources.ContextDirect},
	"prompt_thinking": {Code: "bfcl_prompt_thinking", Name: "BFCL prompted tool use with thinking", Kind: sources.ContextDirect},
}

// Collector is the BFCL collector.
type Collector struct {
	URL string
}

// New returns a collector reading the published leaderboard file.
func New() *Collector { return &Collector{URL: DataURL} }

// Info describes the source.
func (c *Collector) Info() sources.Info {
	return sources.Info{
		Code:      SourceCode,
		Name:      "Berkeley Function-Calling Leaderboard (BFCL)",
		Kind:      "builtin",
		Homepage:  "https://gorilla.cs.berkeley.edu/leaderboard.html",
		URL:       c.URL,
		Retrieval: "http_csv",
		AccessBasis: "Leaderboard data file published in the Gorilla project's public GitHub repository (gh-pages branch) " +
			"and fetched from raw.githubusercontent.com; the repository is licensed Apache-2.0.",
		License:     "Apache-2.0 (repository licence)",
		Attribution: "Berkeley Function-Calling Leaderboard, Gorilla project (UC Berkeley).",
	}
}

// Metrics lists the metric this collector emits.
func (c *Collector) Metrics() []metrics.Definition {
	return []metrics.Definition{{
		Key: Metric, DisplayName: "BFCL overall accuracy",
		Description: "Berkeley Function-Calling Leaderboard overall accuracy percentage: tool/function-calling correctness across " +
			"single-turn, live and multi-turn categories. A direct model evaluation; the evaluation context names the mode " +
			"(native function calling or prompted, with or without thinking), or records that the leaderboard stated none. " +
			"The published leaderboard can lag current model releases.",
		ValueKind: metrics.KindNumber, Unit: "percent", Direction: metrics.HigherIsBetter,
		Scope: metrics.ScopeEvidence, DefinedBy: SourceCode, MissingPossible: true, Status: metrics.StatusCurrent,
	}}
}

// Contexts lists the contexts this collector is known to emit before any
// collection. A mode the leaderboard names but this build does not curate gets
// its own derived context, returned in the Result.
func (c *Collector) Contexts() []sources.EvaluationContext {
	out := make([]sources.EvaluationContext, 0, len(modes)+1)
	for _, key := range []string{"fc", "prompt", "fc_thinking", "prompt_thinking"} {
		out = append(out, modes[key])
	}
	return append(out, unstatedMode())
}

func unstatedMode() sources.EvaluationContext {
	return sources.EvaluationContext{Code: UnstatedModeContext, Name: "BFCL evaluation, mode not stated", Kind: sources.ContextDirect}
}

// resolveMode resolves a published mode qualifier to a context. An absent
// qualifier is its own context rather than an error: the row is valid
// evidence, and what it lacks is a statement of mode.
func resolveMode(mode string) (sources.EvaluationContext, error) {
	if sources.FoldContextLabel(mode) == "" {
		return unstatedMode(), nil
	}
	if ec, ok := modes[sources.FoldContextLabel(mode)]; ok {
		return ec, nil
	}
	derived, err := sources.DeriveContext(derivedModePrefix, mode, sources.ContextDirect)
	if err != nil {
		return sources.EvaluationContext{}, err
	}
	derived.Name = "BFCL " + derived.Name
	return derived, nil
}

// Collect downloads and parses the leaderboard file.
func (c *Collector) Collect(ctx context.Context, env sources.Env) (sources.Result, error) {
	body, err := env.HTTP.Get(ctx, c.URL)
	if err != nil {
		return sources.Result{}, err
	}
	return Parse(body)
}

// formatChange reports that the leaderboard file no longer has the shape this
// collector reads, so the previous refresh's evidence is kept.
func formatChange(format string, args ...any) error {
	return fmt.Errorf("%s. The BFCL leaderboard file's published format appears to have changed, so this version of devmodels cannot read it "+
		"faithfully; the evidence from the last successful refresh is unchanged, and reading the new format may need a devmodels update",
		fmt.Sprintf(format, args...))
}

// Parse reads the overall-results CSV. Columns are located by header name
// only: a reshaped file fails rather than being read positionally.
func Parse(raw []byte) (sources.Result, error) {
	reader := csv.NewReader(bytes.NewReader(raw))
	reader.FieldsPerRecord = -1
	rows, err := reader.ReadAll()
	if err != nil {
		return sources.Result{}, fmt.Errorf("reading BFCL CSV: %w", err)
	}
	if len(rows) < 2 {
		return sources.Result{}, formatChange("the BFCL CSV has no data rows")
	}

	accuracyCol, modelCol := -1, -1
	for i, h := range rows[0] {
		switch strings.ToLower(strings.TrimSpace(strings.TrimPrefix(h, string(rune(0xFEFF))))) {
		case "overall acc", "overall accuracy":
			accuracyCol = i
		case "model":
			modelCol = i
		}
	}
	if accuracyCol == -1 || modelCol == -1 {
		return sources.Result{}, formatChange("the BFCL CSV has no 'Overall Acc' or 'Model' column (header %v)", rows[0])
	}

	result := sources.Result{SourceRowCount: len(rows) - 1}
	for i, row := range rows[1:] {
		ref := fmt.Sprintf("data_overall.csv:%d", i+2)
		if accuracyCol >= len(row) || modelCol >= len(row) {
			result.Rejected = append(result.Rejected, sources.RejectedRow{
				SourceModelName: strings.Join(row, ","), SourceRowRef: ref,
				Reason: "row has fewer columns than the header declares",
			})
			continue
		}
		published := strings.TrimSpace(row[modelCol])
		if published == "" {
			result.Rejected = append(result.Rejected, sources.RejectedRow{SourceModelName: "(no model name)", SourceRowRef: ref, Reason: "row has no model name"})
			continue
		}

		accuracy := strings.TrimSpace(row[accuracyCol])
		score, err := strconv.ParseFloat(strings.TrimSuffix(accuracy, "%"), 64)
		// Written so that NaN, which ParseFloat accepts, fails the check too.
		if err != nil || !(score >= 0 && score <= 100) {
			result.Rejected = append(result.Rejected, sources.RejectedRow{
				SourceModelName: published, SourceRowRef: ref,
				Reason:  fmt.Sprintf("accuracy %q is not a percentage", accuracy),
				Details: map[string]string{"metric": Metric},
			})
			continue
		}

		name, mode := splitMode(published)
		ctx, err := resolveMode(mode)
		if err != nil {
			result.Rejected = append(result.Rejected, sources.RejectedRow{
				SourceModelName: published, SourceRowRef: ref,
				Reason:  fmt.Sprintf("%v, so this value cannot be attributed to an evaluation context", err),
				Details: map[string]string{"metric": Metric, "score": accuracy},
			})
			continue
		}
		result.AddContext(ctx)

		variant := identity.Classify(identity.NormaliseIdentifier(name))
		result.AcceptedRows++
		result.Observations = append(result.Observations, sources.Observation{
			Metric:            Metric,
			SourceModelName:   published,
			SourceRowRef:      ref,
			BaseKey:           variant.BaseKey(),
			Effort:            variant.Effort,
			Serving:           variant.Serving,
			ContextVariant:    variant.ContextVariant,
			EvaluationContext: ctx.Code,
			Value:             sources.NumberValue(score),
		})
	}
	return result, result.AccountingError(SourceCode)
}

// splitMode separates a trailing "(mode)" qualifier from the model name,
// verbatim: normalising it is FoldContextLabel's job, not this function's.
func splitMode(s string) (name, mode string) {
	s = strings.TrimSpace(s)
	if !strings.HasSuffix(s, ")") {
		return s, ""
	}
	open := strings.LastIndex(s, "(")
	if open == -1 {
		return s, ""
	}
	return strings.TrimSpace(s[:open]), strings.TrimSpace(s[open+1 : len(s)-1])
}
