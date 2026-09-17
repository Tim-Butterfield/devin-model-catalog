// Package epoch collects benchmark results from Epoch AI's benchmarking data.
//
// Epoch AI publishes every benchmark it tracks as a CSV inside one public zip
// archive, under Creative Commons Attribution 4.0. Its rows carry the
// reasoning effort a model was measured at and, for agentic benchmarks, the
// harness that produced the score — the two dimensions that let evidence land
// on Devin's effort-specific variants and let consumers see which harness a
// number describes.
//
// Collection is deliberately narrow in what it reads and deliberately open in
// what it accepts. A benchmark is collected only when it is listed below with
// a metric definition, so the archive gaining files never silently changes what
// the catalog contains; but within those files a harness name this build has
// never seen is carried as its own distinct context rather than discarded, so
// the field gaining an agent does not need a devmodels release.
package epoch

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Tim-Butterfield/devin-model-catalog/internal/identity"
	"github.com/Tim-Butterfield/devin-model-catalog/internal/metrics"
	"github.com/Tim-Butterfield/devin-model-catalog/internal/sources"
)

// SourceCode identifies this source.
const SourceCode = "epoch"

// ArchiveURL is the published bulk download.
const ArchiveURL = "https://epoch.ai/data/benchmark_data.zip"

// Attribution is required by the CC BY licence wherever this data appears.
const Attribution = "Epoch AI, 'Capabilities & Benchmarking'. Published online at epoch.ai. Retrieved from 'https://epoch.ai/benchmarks'. Licensed CC BY 4.0."

// byteOrderMark is stripped from the first header cell when present.
var byteOrderMark = string(rune(0xFEFF))

// OwnEvaluationContext is the context of benchmarks Epoch AI runs itself
// rather than republishing from a harness leaderboard.
const OwnEvaluationContext = "epoch_ai_eval"

// identifierColumn is the versioned model identifier every collected file
// carries, and the identity a row is read from when it is present.
const identifierColumn = "Model version"

type dataset struct {
	file        string
	metric      string
	scoreColumn string
	// nameColumn is the file's display-name column, if it has one. It is the
	// fallback identity for a row whose versioned identifier is blank: the
	// name is still the source's own name for the model, so reading it
	// recovers real evidence instead of discarding it, and it normalises to
	// the same identity as the versioned form ("SWE-1.7" and "swe-1.7").
	nameColumn    string
	harnessColumn string // empty when Epoch runs the benchmark itself
	effortColumn  string
}

var datasets = []dataset{
	{file: "swe_bench_verified.csv", metric: "swe_bench_verified", scoreColumn: "mean_score"},
	{file: "deepswe_external.csv", metric: "deepswe", scoreColumn: "Pass@1", nameColumn: "Name", harnessColumn: "Harness", effortColumn: "Reasoning effort"},
	{file: "terminalbench_external.csv", metric: "terminal_bench", scoreColumn: "Accuracy mean", nameColumn: "Name", harnessColumn: "Agent"},
	{file: "frontiercode_external.csv", metric: "frontiercode", scoreColumn: "Main score", nameColumn: "Name", harnessColumn: "Harness", effortColumn: "Reasoning effort"},
}

// Collector is the Epoch AI collector.
type Collector struct {
	URL string
}

// New returns a collector reading the published archive.
func New() *Collector { return &Collector{URL: ArchiveURL} }

// Info describes the source.
func (c *Collector) Info() sources.Info {
	return sources.Info{
		Code:      SourceCode,
		Name:      "Epoch AI benchmarking data",
		Kind:      "builtin",
		Homepage:  "https://epoch.ai/benchmarks",
		URL:       c.URL,
		Retrieval: "http_csv_zip",
		AccessBasis: "Public bulk data download published by Epoch AI. The archive's README and the benchmarking hub state the data is " +
			"free to use, distribute and reproduce provided the source and authors are credited (Creative Commons Attribution).",
		License:     "CC BY 4.0",
		Attribution: Attribution,
	}
}

// Metrics lists the metrics this collector emits.
func (c *Collector) Metrics() []metrics.Definition {
	def := func(key, name, desc string) metrics.Definition {
		return metrics.Definition{
			Key: key, DisplayName: name, Description: desc + " Source: Epoch AI (CC BY 4.0).",
			ValueKind: metrics.KindNumber, Unit: "percent", Direction: metrics.HigherIsBetter,
			Scope: metrics.ScopeEvidence, DefinedBy: SourceCode, MissingPossible: true, Status: metrics.StatusCurrent,
		}
	}
	return []metrics.Definition{
		def("swe_bench_verified", "SWE-bench Verified", "Percentage of SWE-bench Verified GitHub issues resolved, from Epoch AI's own evaluation runs (mean score). A general software-engineering issue-resolution measure; evaluation context epoch_ai_eval."),
		def("deepswe", "DeepSWE", "DeepSWE Pass@1 percentage on realistic implementation tasks, as republished by Epoch AI. Each value names the agent harness that produced it."),
		def("terminal_bench", "Terminal-Bench", "Terminal-Bench mean accuracy percentage on agentic command-line tasks, as republished by Epoch AI from the Terminal-Bench leaderboard. Each value names the agent harness that produced it."),
		def("frontiercode", "FrontierCode", "FrontierCode main score percentage on hard real-world coding tasks, as republished by Epoch AI. Each value names the agent harness, including Devin itself where published."),
	}
}

// Contexts lists the contexts this collector is known to emit before any
// collection. A harness the archive names but this build does not curate gets
// its own derived context, returned in the Result.
func (c *Collector) Contexts() []sources.EvaluationContext {
	out := []sources.EvaluationContext{{Code: OwnEvaluationContext, Name: "Epoch AI evaluation setup", Kind: sources.ContextExternalHarness}}
	return append(out, sources.KnownHarnessContexts()...)
}

// Collect downloads and parses the archive.
func (c *Collector) Collect(ctx context.Context, env sources.Env) (sources.Result, error) {
	body, err := env.HTTP.Get(ctx, c.URL)
	if err != nil {
		return sources.Result{}, err
	}
	return Parse(body)
}

// formatChange reports that the archive no longer has the shape this collector
// reads. It fails the whole source rather than yielding a partial result, so
// the previous refresh's evidence is kept and the user is told what to do.
func formatChange(format string, args ...any) error {
	return fmt.Errorf("%s. The Epoch AI archive's published format appears to have changed, so this version of devmodels cannot read it faithfully; "+
		"the evidence from the last successful refresh is unchanged, and reading the new format may need a devmodels update", fmt.Sprintf(format, args...))
}

// Parse reads the archive into observations. A missing expected file, a
// missing column, or an empty file fails the whole source: that is a changed
// source structure, and the previous evidence must be kept.
func Parse(archive []byte) (sources.Result, error) {
	reader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return sources.Result{}, fmt.Errorf("reading Epoch AI archive: %w", err)
	}
	byName := map[string]*zip.File{}
	for _, f := range reader.File {
		name := f.Name
		if i := strings.LastIndex(name, "/"); i != -1 && !strings.HasPrefix(name, "epoch_capabilities_index/") {
			name = name[i+1:]
		}
		byName[name] = f
	}

	var result sources.Result
	result.AddContext(sources.EvaluationContext{Code: OwnEvaluationContext, Name: "Epoch AI evaluation setup", Kind: sources.ContextExternalHarness})
	for _, spec := range datasets {
		file, ok := byName[spec.file]
		if !ok {
			return sources.Result{}, formatChange("the archive no longer contains %s, so %s cannot be collected", spec.file, spec.metric)
		}
		if err := readDataset(&result, file, spec); err != nil {
			return sources.Result{}, err
		}
	}
	return result, result.AccountingError(SourceCode)
}

func readDataset(result *sources.Result, file *zip.File, spec dataset) error {
	handle, err := file.Open()
	if err != nil {
		return fmt.Errorf("opening %s: %w", spec.file, err)
	}
	defer handle.Close()

	reader := csv.NewReader(handle)
	reader.FieldsPerRecord = -1

	header, err := reader.Read()
	if err != nil {
		return fmt.Errorf("reading the header of %s: %w", spec.file, err)
	}
	index := map[string]int{}
	for i, name := range header {
		index[strings.TrimSpace(strings.TrimPrefix(name, byteOrderMark))] = i
	}
	required := []string{identifierColumn, spec.scoreColumn}
	if spec.harnessColumn != "" {
		required = append(required, spec.harnessColumn)
	}
	for _, col := range required {
		if _, ok := index[col]; !ok {
			return formatChange("%s has no %q column", spec.file, col)
		}
	}

	rows := 0
	for {
		row, err := reader.Read()
		if err == io.EOF {
			break
		}
		parseErr, malformed := err.(*csv.ParseError)
		if err != nil && !malformed {
			// Not a malformed row but a failing reader, such as a corrupt
			// archive member: every later read returns the same error.
			return fmt.Errorf("reading %s: %w", spec.file, err)
		}
		rows++
		result.SourceRowCount++
		if malformed {
			// FieldPos is only valid after a successful read.
			result.Rejected = append(result.Rejected, sources.RejectedRow{
				SourceModelName: "(unreadable row)", SourceRowRef: fmt.Sprintf("%s:%d", spec.file, parseErr.StartLine),
				Reason: fmt.Sprintf("row could not be read: %v", err),
			})
			continue
		}
		line, _ := reader.FieldPos(0)
		record(result, row, index, spec, fmt.Sprintf("%s:%d", spec.file, line))
	}
	if rows == 0 {
		return formatChange("%s has no data rows", spec.file)
	}
	return nil
}

func record(result *sources.Result, row []string, index map[string]int, spec dataset, ref string) {
	field := func(col string) string {
		at, ok := index[col]
		if !ok || at >= len(row) {
			return ""
		}
		return strings.TrimSpace(row[at])
	}
	reject := func(name, reason string) {
		result.Rejected = append(result.Rejected, sources.RejectedRow{
			SourceModelName: name, SourceRowRef: ref, Reason: reason,
			Details: map[string]string{"metric": spec.metric, "score": field(spec.scoreColumn)},
		})
	}

	identifier := field(identifierColumn)
	if identifier == "" && spec.nameColumn != "" {
		identifier = field(spec.nameColumn)
	}
	if identifier == "" {
		reject("(no model identifier)", fmt.Sprintf("the row names no model: %s has an empty %q%s, so the value cannot be attributed to one",
			spec.file, identifierColumn, optionalNameNote(spec)))
		return
	}

	raw := field(spec.scoreColumn)
	score, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		reject(identifier, fmt.Sprintf("score %q is not a number, so no %s value is recorded", truncate(raw), spec.metric))
		return
	}
	// Written so that NaN, which ParseFloat accepts, fails the check too.
	if !(score >= 0 && score <= 1) {
		reject(identifier, fmt.Sprintf("score %v is outside the 0-1 fraction range this archive publishes; refusing to guess its scale", score))
		return
	}

	contextCode := OwnEvaluationContext
	if spec.harnessColumn != "" {
		harness, _, err := sources.ResolveHarness(field(spec.harnessColumn))
		if err != nil {
			reject(identifier, fmt.Sprintf("%v, so this agentic %s result cannot be attributed to an evaluation context", err, spec.metric))
			return
		}
		result.AddContext(harness)
		contextCode = harness.Code
	}

	name, effort := splitEffortSuffix(identifier)
	if spec.effortColumn != "" {
		if explicit, ok := identity.ParseEffort(field(spec.effortColumn)); ok {
			effort = explicit
		}
	}
	variant := identity.Classify(identity.NormaliseIdentifier(name))
	if effort == identity.EffortUnspecified {
		effort = variant.Effort
	}

	result.AcceptedRows++
	result.Observations = append(result.Observations, sources.Observation{
		Metric:            spec.metric,
		SourceModelName:   identifier,
		SourceRowRef:      ref,
		BaseKey:           variant.BaseKey(),
		Effort:            effort,
		Serving:           variant.Serving,
		ContextVariant:    variant.ContextVariant,
		EvaluationContext: contextCode,
		// Rounded to strip binary floating-point noise from the scaling
		// (0.781*100 is 78.10000000000001); the archive publishes no more
		// precision than this.
		Value: sources.NumberValue(math.Round(score*100*1e6) / 1e6),
	})
}

func optionalNameNote(spec dataset) string {
	if spec.nameColumn == "" {
		return ""
	}
	return fmt.Sprintf(" and an empty %q", spec.nameColumn)
}

// splitEffortSuffix reads the "_max" / "_xhigh" form Epoch appends to model
// identifiers. "_unknown" means Epoch did not record the effort; it is removed
// as metadata rather than kept as part of the name. Any other suffix stays,
// because an unrecognised token is identity-bearing.
func splitEffortSuffix(identifier string) (string, identity.Effort) {
	at := strings.LastIndex(identifier, "_")
	if at < 0 {
		return identifier, identity.EffortUnspecified
	}
	suffix := strings.ToLower(identifier[at+1:])
	if suffix == "unknown" {
		return identifier[:at], identity.EffortUnspecified
	}
	if effort, ok := identity.ParseEffort(suffix); ok {
		return identifier[:at], effort
	}
	return identifier, identity.EffortUnspecified
}

// truncate keeps an over-long cell out of a rejection reason. It cuts on a rune
// boundary: the reason is stored and returned as JSON, so half a character
// would be a defect in output the user reads.
func truncate(s string) string {
	if len(s) <= 40 {
		return s
	}
	cut := 40
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}
