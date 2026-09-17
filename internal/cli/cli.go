// Package cli implements the devmodels command line. It parses arguments,
// calls app.Service, and formats results; it holds no catalog logic.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"text/tabwriter"

	devmodelcatalog "github.com/Tim-Butterfield/devin-model-catalog"
	"github.com/Tim-Butterfield/devin-model-catalog/internal/app"
	"github.com/Tim-Butterfield/devin-model-catalog/internal/buildinfo"
	"github.com/Tim-Butterfield/devin-model-catalog/internal/config"
	"github.com/Tim-Butterfield/devin-model-catalog/internal/mcpserver"
	"github.com/Tim-Butterfield/devin-model-catalog/internal/query"
)

// Exit codes.
const (
	ExitOK       = 0
	ExitFailure  = 1
	ExitUsage    = 2
	ExitNotReady = 3
	ExitSource   = 4
)

// Env is the process environment the CLI runs in.
type Env struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	Getenv func(string) string

	// Configure adjusts service options before opening; tests use it to
	// inject fetchers and clocks.
	Configure func(*app.Options)
}

// Run executes one invocation and returns its exit code.
func Run(ctx context.Context, args []string, env Env) int {
	if env.Getenv == nil {
		env.Getenv = os.Getenv
	}
	if env.Stdin == nil {
		env.Stdin = os.Stdin
	}
	if len(args) == 0 {
		fmt.Fprint(env.Stderr, usage)
		return ExitUsage
	}
	c := &command{env: env, ctx: ctx}
	switch args[0] {
	case "help", "-h", "--help":
		fmt.Fprint(env.Stdout, usage)
		return ExitOK
	case "version", "--version":
		fmt.Fprintln(env.Stdout, "devmodels", buildinfo.String())
		return ExitOK
	case "agents-md":
		// Exactly the embedded bytes: nothing added, nothing removed.
		io.WriteString(env.Stdout, devmodelcatalog.AgentsMD)
		return ExitOK
	}

	handlers := map[string]func([]string) error{
		"datasets":    c.datasets,
		"use-dataset": c.useDataset,
		"refresh":     c.refresh,
		"status":      c.status,
		"metrics":     c.metrics,
		"query":       c.query,
		"model":       c.model,
		"unmatched":   c.unmatched,
		"rejected":    c.rejected,
		"alias":       c.alias,
		"import":      c.importCmd,
		"browser":     c.browser,
		"config":      c.config,
		"mcp":         c.mcp,
	}
	handler, ok := handlers[args[0]]
	if !ok {
		fmt.Fprintf(env.Stderr, "devmodels: unknown command %q\n\n%s", args[0], usage)
		return ExitUsage
	}
	return c.exit(handler(args[1:]))
}

type command struct {
	env  Env
	ctx  context.Context
	db   string
	json bool
	svc  *app.Service
}

type usageError struct{ msg string }

func (u *usageError) Error() string { return u.msg }

func usagef(format string, args ...any) error { return &usageError{fmt.Sprintf(format, args...)} }

func (c *command) exit(err error) int {
	if c.svc != nil {
		c.svc.Close()
	}
	if err == nil {
		return ExitOK
	}
	if errors.Is(err, flag.ErrHelp) {
		return ExitOK
	}
	code := ExitFailure
	var u *usageError
	if errors.As(err, &u) {
		code = ExitUsage
	} else {
		switch app.KindOf(err) {
		case app.KindUsage:
			code = ExitUsage
		case app.KindNotReady:
			code = ExitNotReady
		case app.KindSource:
			code = ExitSource
		}
	}
	if c.json {
		c.writeJSON(map[string]any{"error": map[string]any{"exit_code": code, "message": err.Error()}})
	}
	// An error is prose and wraps like prose. Some carry a list — the valid
	// metric keys, the current datasets — which is exactly the case where an
	// unwrapped line is least readable.
	for _, line := range wrap(err.Error(), "devmodels: ", "  ") {
		fmt.Fprintln(c.env.Stderr, line)
	}
	return code
}

// flags builds a flag set with the common --db and --json flags.
//
// --help answers with the command's own help rather than with a list of flag
// defaults, which says nothing about what the command is for or what it takes
// besides flags.
func (c *command) flags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet("devmodels "+name, flag.ContinueOnError)
	fs.SetOutput(c.env.Stderr)
	fs.Usage = func() { fmt.Fprint(c.env.Stderr, helpFor(name)) }
	fs.StringVar(&c.db, "db", "", "database path (overrides DEVMODELS_DB and the config file)")
	fs.BoolVar(&c.json, "json", false, "write machine-readable JSON to stdout")
	return fs
}

// parse accepts flags before, between and after positional arguments.
func parse(fs *flag.FlagSet, args []string) ([]string, error) {
	var flags, positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			positional = append(positional, a)
			continue
		}
		flags = append(flags, a)
		name := strings.TrimLeft(a, "-")
		if strings.Contains(name, "=") {
			continue
		}
		if f := fs.Lookup(name); f != nil {
			if b, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && b.IsBoolFlag() {
				continue
			}
			if i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
		}
	}
	if err := fs.Parse(flags); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil, err
		}
		return nil, usagef("%v", err)
	}
	return positional, nil
}

func (c *command) resolved() (config.Resolved, error) {
	return config.Load(c.db, c.env.Getenv)
}

func (c *command) service() (*app.Service, error) {
	cfg, err := c.resolved()
	if err != nil {
		return nil, err
	}
	opts := app.Options{Config: cfg}
	if c.env.Configure != nil {
		c.env.Configure(&opts)
	}
	svc, err := app.Open(c.ctx, opts)
	if err != nil {
		return nil, err
	}
	c.svc = svc
	return svc, nil
}

func (c *command) writeJSON(v any) error {
	enc := json.NewEncoder(c.env.Stdout)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

func (c *command) table() *tabwriter.Writer {
	return tabwriter.NewWriter(c.env.Stdout, 0, 0, 2, ' ', 0)
}

func (c *command) printf(format string, args ...any) { fmt.Fprintf(c.env.Stdout, format, args...) }

// wrapped prints text laid out for Width columns, prefixing the first line with
// first and any continuation with rest. It is how every piece of prose this
// program composes reaches the terminal, so the wrapping is the program's
// decision and not the window's.
func (c *command) wrapped(first, rest, text string) {
	for _, line := range wrap(text, first, rest) {
		fmt.Fprintln(c.env.Stdout, line)
	}
}

// ---- datasets ---------------------------------------------------------------

func (c *command) datasets(args []string) error {
	fs := c.flags("datasets")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 1 || (len(pos) == 1 && pos[0] != "refresh") {
		return usagef("usage: devmodels datasets [refresh] [--json]")
	}
	svc, err := c.service()
	if err != nil {
		return err
	}
	var list *app.DatasetList
	if len(pos) == 1 {
		list, err = svc.RefreshDatasets(c.ctx)
	} else {
		list, err = svc.Datasets(c.ctx)
	}
	if err != nil {
		return err
	}
	if c.json {
		return c.writeJSON(list)
	}
	if list.Inventory == nil {
		c.printf("No dataset inventory yet. Run: devmodels datasets refresh\n")
		return nil
	}
	c.printf("Dataset inventory refreshed %s\n", list.Inventory.RefreshedAt)
	c.wrapped("from ", "  ", list.Inventory.SourceURL)
	c.printf("\n")
	w := c.table()
	fmt.Fprintln(w, "ID\tDATASET\tMODEL ROWS\tSELECTABLE\tSELECTED")
	for _, d := range list.Datasets {
		selectable, rows := "yes", strconv.Itoa(d.ModelRowCount)
		if !d.Supported {
			selectable, rows = "no: "+d.UnsupportedReason, "-"
		}
		mark := ""
		if d.ID == list.SelectedDatasetID {
			mark = "*"
		}
		fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\n", d.ID, d.DisplayName, rows, selectable, mark)
	}
	w.Flush()
	if list.Selected != nil && !list.SelectedInInventory {
		c.printf("\n")
		c.wrapped("Selected: ", "  ", list.Selected.DisplayName+" (not in the current inventory)")
	}
	for _, warn := range list.Warnings {
		c.wrapped("warning: ", "  ", warn)
	}
	if list.Selected == nil {
		c.printf("\nSelect one with: devmodels use-dataset <id>\n")
	}
	return nil
}

func (c *command) useDataset(args []string) error {
	fs := c.flags("use-dataset")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usagef("usage: devmodels use-dataset <id> [--json]")
	}
	id, err := strconv.Atoi(pos[0])
	if err != nil || id < 1 {
		return usagef("dataset id must be a positive integer from `devmodels datasets`, not %q", pos[0])
	}
	svc, err := c.service()
	if err != nil {
		return err
	}
	sel, err := svc.UseDataset(c.ctx, id)
	if err != nil {
		return err
	}
	if c.json {
		return c.writeJSON(sel)
	}
	c.wrapped("", "  ", sel.Message)
	return nil
}

func (c *command) refresh(args []string) error {
	fs := c.flags("refresh")
	sourceList := fs.String("source", "", "comma-separated source codes to refresh (default: all)")
	allowShrink := fs.Bool("allow-shrink", false, "accept a catalog less than half the current size")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	svc, err := c.service()
	if err != nil {
		return err
	}
	opts := app.RefreshOptions{AllowShrink: *allowShrink}
	for _, s := range strings.Split(*sourceList, ",") {
		if s = strings.TrimSpace(s); s != "" {
			opts.Sources = append(opts.Sources, s)
		}
	}
	report, err := svc.Refresh(c.ctx, opts)
	if report != nil {
		if c.json {
			if jerr := c.writeJSON(report); jerr != nil {
				return jerr
			}
			if err != nil {
				// The report is the JSON document; the error goes to stderr only.
				c.json = false
			}
		} else {
			c.printRefresh(report)
		}
	}
	return err
}

// printRefresh writes the human form of a refresh report.
//
// It is deliberately narrow. The table carries the row accounting only —
// PUBLISHED = ACCEPTED + EXCLUDED + REJECTED, with UNMATCHED outside that sum —
// and the per-source data-point count, which is the one number nobody reads
// off a refresh, is left to `--json`. What follows the table is a line per
// thing there is actually something to do about, so a clean refresh ends at
// the table. Every line is written to sit inside 80 columns at the sizes these
// sources produce; nothing here inspects the terminal.
func (c *command) printRefresh(report *app.RefreshReport) {
	if report.Dataset != nil {
		if report.CatalogModels != nil {
			c.printf("Dataset: %s (%d models, %s)\n\n", report.Dataset.DisplayName, *report.CatalogModels, report.Dataset.CatalogState)
		} else {
			c.printf("Dataset: %s (catalog %s)\n\n", report.Dataset.DisplayName, report.Dataset.CatalogState)
		}
	}
	w := c.table()
	fmt.Fprintln(w, "SOURCE\tSTATUS\tPUBLISHED\tACCEPTED\tEXCLUDED\tREJECTED\tUNMATCHED")
	for _, s := range report.Sources {
		fmt.Fprintf(w, "%s\t%s\t%d\t%d\t%d\t%d\t%s\n", s.Source, s.Status, s.Counts.Published, s.Counts.Accepted,
			s.Counts.Excluded, s.Counts.Rejected, optionalCount(s.Unmatched))
	}
	w.Flush()

	rejected, rejectedSource := 0, ""
	unmatched := 0
	for _, s := range report.Sources {
		if s.Counts.Rejected > 0 {
			rejected += s.Counts.Rejected
			if rejectedSource == "" {
				rejectedSource = s.Source
			} else if rejectedSource != s.Source {
				// More than one source owns rejected rows, so no single
				// --source would show all of them.
				rejectedSource = "-"
			}
		}
		if s.Unmatched != nil {
			unmatched += *s.Unmatched
		}
	}
	if rejected > 0 || unmatched > 0 {
		c.printf("\n")
	}
	if rejected > 0 {
		rows := "rows"
		if rejected == 1 {
			rows = "row"
		}
		line := fmt.Sprintf("%d published %s could not be represented: `devmodels rejected`", rejected, rows)
		if rejectedSource != "" && rejectedSource != "-" {
			// Naming the one source that owns them saves a step, but only
			// while the whole line still fits; otherwise the bare command is
			// the more useful of the two.
			if full := fmt.Sprintf("%d published %s could not be represented: `devmodels rejected --source %s`",
				rejected, rows, rejectedSource); len(full) <= 80 {
				line = full
			}
		}
		c.printf("%s\n", line)
	}
	if unmatched > 0 {
		c.printf("%d data points describe models this dataset does not offer, which is\n"+
			"normal for a leaderboard. List them with `devmodels unmatched`.\n", unmatched)
	}
	for _, s := range report.Sources {
		for _, warn := range s.Warnings {
			c.wrapped("warning ("+s.Source+"): ", "  ", warn)
		}
	}
}

// ---- status and metrics -----------------------------------------------------

func (c *command) status(args []string) error {
	fs := c.flags("status")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	svc, err := c.service()
	if err != nil {
		return err
	}
	st, err := svc.Status(c.ctx)
	if err != nil {
		return err
	}
	if c.json {
		return c.writeJSON(st)
	}
	usable := "no"
	if st.Usable {
		usable = "yes"
	}
	c.printf("devmodels %s\n", st.Version)
	// A database path is a raw value and routinely longer than the width, so it
	// gets its own line and wraps rather than being cut: a path the reader
	// cannot copy whole is of no use to them.
	c.wrapped("Database: ", "  ", st.Paths.DBPath)
	c.printf("  (%s, schema %d)\n", st.Paths.DBPathSource, st.SchemaVersion)
	c.printf("Usable: %s\n", usable)
	if st.Inventory != nil {
		c.printf("Dataset inventory: %d datasets, refreshed %s\n", st.Inventory.Count, st.Inventory.RefreshedAt)
	} else {
		c.printf("Dataset inventory: none\n")
	}
	if st.Selection != nil {
		c.wrapped("Selected dataset: ", "  ", st.Selection.DisplayName+" ["+st.Selection.SourceKey+"]")
		catalog := string(st.Selection.CatalogState)
		if st.Selection.CatalogRefreshedAt != "" {
			catalog += ", refreshed " + st.Selection.CatalogRefreshedAt
		}
		catalog += fmt.Sprintf(", %d models", st.CatalogModels)
		if len(st.CatalogRows) > 0 {
			var parts []string
			for _, k := range []string{"accepted", "excluded", "rejected"} {
				if n := st.CatalogRows[k]; n > 0 {
					parts = append(parts, fmt.Sprintf("%s %d", k, n))
				}
			}
			catalog += " (rows: " + strings.Join(parts, ", ") + ")"
		}
		c.wrapped("Catalog: ", "  ", catalog)
	} else {
		c.printf("Selected dataset: none\n")
	}
	c.printf("\n")
	// Two ISO timestamps and four other columns do not fit the width together,
	// and "DATA POINTS" is not available to shorten: it is the one name every
	// surface uses for this count. The timestamps move to a line of their own
	// instead, which keeps the counts aligned for scanning and loses nothing.
	w := c.table()
	fmt.Fprintln(w, "SOURCE\tSTATUS\tDATA POINTS\tREJECTED")
	for _, s := range st.Sources {
		status := "-"
		if s.LastAttempt != nil {
			status = s.LastAttempt.Status
		}
		fmt.Fprintf(w, "%s\t%s\t%d\t%d\n", s.Info.Code, status, s.DataPoints, s.RejectedRows)
	}
	w.Flush()
	for _, s := range st.Sources {
		attempt, success := "never", "never"
		if s.LastAttempt != nil {
			attempt = s.LastAttempt.StartedAt
		}
		if s.LastSuccess != nil {
			success = s.LastSuccess.FinishedAt
		}
		c.printf("  %s: last attempt %s, last success %s\n", s.Info.Code, attempt, success)
	}
	c.printf("\n")
	c.wrapped("", "  ", fmt.Sprintf("Data points about models this dataset does not offer: %d (`devmodels unmatched`); aliases: %d", st.Evidence.Unmatched, st.Aliases))
	if st.Evidence.Rejected > 0 {
		c.wrapped("", "  ", fmt.Sprintf("Published rows this version could not represent: %d (`devmodels rejected`)", st.Evidence.Rejected))
	}
	if st.BrowserRuntime != nil {
		platform := ""
		if st.BrowserRuntime.Platform != "" {
			platform = ", " + st.BrowserRuntime.Platform
		}
		c.wrapped("Browser runtime: ", "  ", fmt.Sprintf("%s (Chrome for Testing %s%s)", st.BrowserRuntime.Detail, st.BrowserRuntime.PinnedVersion, platform))
	}
	if len(st.Problems) > 0 {
		c.printf("\nProblems:\n")
		for _, p := range st.Problems {
			c.wrapped("  - ", "    ", p)
		}
	}
	return nil
}

func (c *command) metrics(args []string) error {
	fs := c.flags("metrics")
	detail := fs.String("detail", "", "summary (default) or full")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	svc, err := c.service()
	if err != nil {
		return err
	}
	d, err := svc.Describe(c.ctx, *detail)
	if err != nil {
		return err
	}
	if c.json {
		return c.writeJSON(d)
	}
	if d.Dataset != nil {
		c.printf("Dataset: %s (catalog %s, %d models)\n", d.Dataset.DisplayName, d.Dataset.CatalogState, d.CatalogModels)
	}

	// A metric key can run to nearly fifty columns, and there are seven further
	// facts about it. As a single table that cannot fit the width, and the key
	// is the part a caller has to type exactly, so it gets its own line and the
	// rest is described beneath it. Grouping by scope also removes a column:
	// every metric under a heading shares it.
	for _, group := range []struct {
		scope string
		title string
		about string
	}{
		{"catalog", "Catalog metrics", "published by the dataset for each of its models"},
		{"evidence", "Evidence metrics", "third-party benchmarks matched to models by identity"},
	} {
		var inScope []query.MetricDescription
		for _, m := range d.Metrics {
			if string(m.Scope) == group.scope {
				inScope = append(inScope, m)
			}
		}
		if len(inScope) == 0 {
			continue
		}
		c.printf("\n%s — %s\n\n", group.title, group.about)
		for _, m := range inScope {
			c.printf("  %s\n", m.Key)
			c.printf("      %s\n", metricFacts(m))
			c.printf("      %s\n", metricCoverage(m, d.CatalogModels))
			if d.Detail == "full" && m.Description != "" {
				for _, line := range wrap(m.Description, "      ", "      ") {
					c.printf("%s\n", line)
				}
			}
		}
	}

	if len(d.MetricsWithoutValues) > 0 {
		for _, line := range wrap("No values in this dataset: "+strings.Join(d.MetricsWithoutValues, ", "), "\n", "") {
			c.printf("%s\n", line)
		}
	}
	c.printf("\nSources:\n")
	for _, s := range d.Sources {
		c.printf("  %s — %s\n", s.Code, s.Name)
		if s.AccessBasis != "" {
			for _, line := range wrap(s.AccessBasis, "      access basis: ", "        ") {
				c.printf("%s\n", line)
			}
		}
		if s.Attribution != "" {
			for _, line := range wrap(s.Attribution, "      attribution: ", "        ") {
				c.printf("%s\n", line)
			}
		}
	}
	if d.Detail == "summary" {
		c.printf("\nUse --detail full for descriptions, data point counts, source\n")
		c.printf("access bases, licences and attribution.\n")
	}
	if !d.CatalogReady {
		c.printf("\n")
		c.wrapped("Catalog not ready: ", "  ", d.NotReadyReason)
	}
	return nil
}

// ---- query ------------------------------------------------------------------

type multi []string

func (m *multi) String() string     { return strings.Join(*m, ",") }
func (m *multi) Set(v string) error { *m = append(*m, v); return nil }

var (
	exprPattern  = regexp.MustCompile(`^\s*([a-z][a-z0-9_]*)\s*(>=|<=|!=|==|=|>|<)\s*(.+?)\s*$`)
	barePattern  = regexp.MustCompile(`^\s*([a-z][a-z0-9_]*)\s*$`)
	orderPattern = regexp.MustCompile(`^\s*([a-z][a-z0-9_]*)\s*(?::\s*(asc|desc))?\s*$`)
	opNames      = map[string]string{">=": "gte", ">": "gt", "<=": "lte", "<": "lt", "=": "eq", "==": "eq", "!=": "ne"}
)

func parseCriterion(expr, missing string) (query.Criterion, error) {
	if m := barePattern.FindStringSubmatch(expr); m != nil {
		if missing == query.MissingAllow {
			return query.Criterion{}, usagef("--where-or-missing %q: a bare metric means present, which cannot allow missing", expr)
		}
		return query.Criterion{Metric: m[1], Op: "present"}, nil
	}
	m := exprPattern.FindStringSubmatch(expr)
	if m == nil || strings.ContainsAny(m[3][:1], "<>=!") {
		return query.Criterion{}, usagef("cannot parse criterion %q: use metric>=value, metric<value, metric=value, metric!=value, or a bare metric for present", expr)
	}
	return query.Criterion{Metric: m[1], Op: opNames[m[2]], Value: parseValue(m[3]), Missing: missing}, nil
}

// parseValue reads a criterion value: true or false, a number with an optional
// K or M multiplier (200K), or otherwise text exactly as written. A quoted value
// is always text, so 'medium' or "5k" can be compared with a text metric.
func parseValue(s string) any {
	if len(s) >= 2 && (s[0] == '"' || s[0] == '\'') && s[len(s)-1] == s[0] {
		return s[1 : len(s)-1]
	}
	switch strings.ToLower(s) {
	case "true":
		return true
	case "false":
		return false
	}
	number, mult := s, 1.0
	switch lower := strings.ToLower(s); {
	case len(s) > 1 && strings.HasSuffix(lower, "m"):
		number, mult = s[:len(s)-1], 1_000_000
	case len(s) > 1 && strings.HasSuffix(lower, "k"):
		number, mult = s[:len(s)-1], 1_000
	}
	if f, err := strconv.ParseFloat(number, 64); err == nil {
		return f * mult
	}
	return s
}

func (c *command) query(args []string) error {
	fs := c.flags("query")
	var where, whereOrMissing, order, providers, efforts, serving, contextVariants, contexts, preferContexts multi
	fs.Var(&contexts, "context", "evaluation context restriction")
	fs.Var(&preferContexts, "prefer-context", "evaluation context preference")
	allowAmbiguous := fs.Bool("allow-ambiguous", false, "keep models whose peer values disagree about a criterion")
	fs.Var(&where, "where", "criterion; missing rejects")
	fs.Var(&whereOrMissing, "where-or-missing", "criterion; missing allowed")
	fs.Var(&order, "order", "ordering term")
	fs.Var(&providers, "provider", "provider filter")
	fs.Var(&efforts, "effort", "effort filter")
	fs.Var(&serving, "serving", "serving variant filter")
	fs.Var(&contextVariants, "context-variant", "context variant filter")
	label := fs.String("label", "", "label substring filter")
	limit := fs.Int("limit", 0, "maximum models")
	exactEffort := fs.Bool("exact-effort", false, "require effort-exact values")
	alternatives := fs.Bool("alternatives", false, "include alternatives")
	detail := fs.String("detail", "", "compact, summary or full")
	requestFile := fs.String("request", "", "JSON request file")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return usagef("unexpected argument %q\n%s", pos[0], helpFor("query"))
	}

	var req query.Request
	if *requestFile != "" {
		var raw []byte
		if *requestFile == "-" {
			raw, err = io.ReadAll(c.env.Stdin)
		} else {
			raw, err = os.ReadFile(*requestFile)
		}
		if err != nil {
			return usagef("reading the request: %v", err)
		}
		dec := json.NewDecoder(strings.NewReader(string(raw)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil {
			return usagef("invalid JSON request: %v", err)
		}
		if err := dec.Decode(&struct{}{}); err != io.EOF {
			return usagef("invalid JSON request: the request must be exactly one JSON object")
		}
	}
	for _, e := range where {
		cr, err := parseCriterion(e, query.MissingReject)
		if err != nil {
			return err
		}
		req.Criteria = append(req.Criteria, cr)
	}
	for _, e := range whereOrMissing {
		cr, err := parseCriterion(e, query.MissingAllow)
		if err != nil {
			return err
		}
		req.Criteria = append(req.Criteria, cr)
	}
	for _, o := range order {
		m := orderPattern.FindStringSubmatch(o)
		if m == nil {
			return usagef("cannot parse --order %q: use metric or metric:asc or metric:desc", o)
		}
		req.OrderBy = append(req.OrderBy, query.OrderTerm{Metric: m[1], Direction: m[2]})
	}
	if *allowAmbiguous {
		for i := range req.Criteria {
			req.Criteria[i].Ambiguous = query.AmbiguousAllow
		}
	}
	req.Filter.Providers = append(req.Filter.Providers, providers...)
	req.Filter.Efforts = append(req.Filter.Efforts, efforts...)
	req.Filter.ServingVariants = append(req.Filter.ServingVariants, serving...)
	req.Filter.ContextVariants = append(req.Filter.ContextVariants, contextVariants...)
	if *label != "" {
		req.Filter.LabelContains = *label
	}
	if *limit != 0 {
		req.Limit = *limit
	}
	if *alternatives {
		req.IncludeAlternatives = true
	}
	if *detail != "" {
		req.Detail = *detail
	}

	svc, err := c.service()
	if err != nil {
		return err
	}
	if len(contexts) > 0 || len(preferContexts) > 0 || *exactEffort {
		if err := applyEvidenceFlags(c.ctx, svc, &req, contexts, preferContexts, *exactEffort); err != nil {
			return err
		}
	}
	resp, err := svc.Query(c.ctx, req)
	if err != nil {
		return err
	}
	if c.json {
		return c.writeJSON(resp)
	}

	c.wrapped("Dataset: ", "  ", fmt.Sprintf("%s (catalog refreshed %s)", resp.Dataset.DisplayName, resp.Dataset.CatalogRefreshedAt))
	c.wrapped("", "  ", fmt.Sprintf("%d eligible of %d models (%d matched filters, %d excluded by criteria); showing %d",
		resp.Eligible, resp.CatalogModels, resp.MatchedFilter, resp.ExcludedModels, resp.Returned))
	c.printf("\n")

	// Metric keys are far too wide to head a column, so the value columns are
	// numbered and the numbers are explained under the results. That keeps the
	// rows narrow without hiding which metric a column holds.
	//
	// A request can name more metric terms than a row has room for. Past that
	// point the results are stacked instead, one model at a time, because the
	// alternative is a table whose columns have all been squeezed past the
	// width at which they still say anything.
	columns := queryColumns(req)
	marked := false
	if queryTableFits(len(columns)) {
		marked = c.printQueryTable(resp.Results, columns)
	} else {
		marked = c.printQueryStacked(resp.Results, columns)
	}
	if len(columns) > 0 {
		c.printf("\n")
		for i, col := range columns {
			c.wrapped(fmt.Sprintf("[%d] ", i+1), "    ", col.label)
		}
	}
	if marked {
		c.printf("\n")
		c.printf("* proxy (not measured with Devin)\n")
		c.printf("~ inexact variant (effort or serving borrowed)\n")
		c.printf("? ambiguous (peers disagree; range shown)\n")
		c.printf("— missing\n")
	}
	if len(resp.Ambiguities) > 0 {
		c.printf("\nAmbiguous (no single value selected):\n")
		for _, a := range resp.Ambiguities {
			advice := "resolve with --context or --prefer-context"
			if !contains(a.Reasons, query.AmbiguityPeerContexts) {
				advice = "one context publishes different values; inspect the peers with `devmodels model <uid>`"
			} else if contains(a.Reasons, query.AmbiguityConflictingValues) {
				advice = "some resolve with --context or --prefer-context; others are one context publishing different values"
			}
			c.wrapped("  ", "    ", fmt.Sprintf("%s: %d models, peer contexts %s — %s", a.Metric, a.Models, strings.Join(a.Contexts, ", "), advice))
		}
	}
	if len(resp.Exclusions) > 0 {
		c.printf("\nExcluded:\n")
		for _, e := range resp.Exclusions {
			target := ""
			if e.Target != nil {
				target = fmt.Sprintf(" %v", e.Target)
			}
			c.wrapped("  ", "    ", fmt.Sprintf("%d models: %s %s%s (%s)", e.Models, e.Metric, e.Op, target, e.Reason))
		}
	}
	// The notes explain the response's own shape and can run to several
	// sentences; they are prose and wrap as prose.
	for _, n := range resp.Notes {
		c.printf("\n")
		c.wrapped("note: ", "  ", n)
	}
	return nil
}

// printQueryTable writes one row per model — rank, label, uid, then one
// numbered value column per query term — and reports whether any value carried
// an evidence flag, so the caller knows to print the key.
//
// The columns are sized to the results in hand rather than to a guess, so the
// uid column is only as wide as the widest uid actually returned.
func (c *command) printQueryTable(results []query.Result, columns []queryColumn) bool {
	widestUID := 0
	for _, r := range results {
		if w := displayWidth(r.Model.UID); w > widestUID {
			widestUID = w
		}
	}
	rankCol, labelCol, uidCol, valueCol := queryBudget(len(columns), widestUID)

	printRow := func(cells []string) {
		// Every cell but the last is padded; padding the last one would only
		// add trailing spaces, which count toward the width and show up as
		// overflow while being invisible on screen.
		for i, cell := range cells {
			if i == len(cells)-1 {
				c.printf("%s\n", cell)
				return
			}
			c.printf("%s", cell)
		}
		c.printf("\n")
	}

	header := []string{pad("RANK", rankCol), pad("MODEL", labelCol), pad("UID", uidCol)}
	for i := range columns {
		header = append(header, pad(fmt.Sprintf("[%d]", i+1), valueCol))
	}
	printRow(header)
	marked := false
	for _, r := range results {
		row := []string{
			pad(strconv.Itoa(r.Rank), rankCol),
			pad(truncate(r.Model.Label, labelCol-1), labelCol),
			pad(truncate(r.Model.UID, uidCol-1), uidCol),
		}
		for _, col := range columns {
			cell, flagged := formatResolution(col.value(r))
			marked = marked || flagged
			row = append(row, pad(truncate(cell, valueCol-1), valueCol))
		}
		printRow(row)
	}
	return marked
}

// printQueryStacked writes the same facts one model at a time, for a request
// with more metric terms than a row can hold.
//
// Everything the table shows is still here and now has a whole line to itself:
// the rank and label head the block, the uid sits under them at full width, and
// each numbered value means exactly what the same number means in the legend
// below. Blocks are separated by a blank line so one model reads as one thing.
func (c *command) printQueryStacked(results []query.Result, columns []queryColumn) bool {
	const indent = "    "
	marked := false
	for i, r := range results {
		if i > 0 {
			c.printf("\n")
		}
		head := pad(strconv.Itoa(r.Rank), len(indent)-1) + " "
		c.printf("%s%s\n", head, truncate(r.Model.Label, Width-displayWidth(head)))
		c.printf("%suid: %s\n", indent, truncate(r.Model.UID, Width-len(indent)-len("uid: ")))
		for k, col := range columns {
			cell, flagged := formatResolution(col.value(r))
			marked = marked || flagged
			prefix := fmt.Sprintf("%s[%d] ", indent, k+1)
			c.printf("%s%s\n", prefix, truncate(cell, Width-displayWidth(prefix)))
		}
	}
	return marked
}

// applyEvidenceFlags applies --context, --prefer-context and --exact-effort to
// the benchmark (evidence-scope) terms of req. Catalog metrics are published by
// the dataset in its own context, so a benchmark context would only make every
// price missing. A term that already names contexts or preferences, as a
// --request file can, keeps them rather than having the flags widen them.
func applyEvidenceFlags(ctx context.Context, svc *app.Service, req *query.Request, contexts, prefer []string, exactEffort bool) error {
	d, err := svc.Describe(ctx, query.DetailFull)
	if err != nil {
		return err
	}
	evidence := map[string]bool{}
	for _, m := range d.Metrics {
		evidence[m.Key] = m.Scope == "evidence"
	}
	apply := func(metric string, ev *query.EvidencePolicy) {
		if !evidence[metric] {
			return
		}
		ev.ExactEffort = ev.ExactEffort || exactEffort
		if len(ev.Contexts) == 0 {
			ev.Contexts = append([]string(nil), contexts...)
		}
		if len(ev.PreferContexts) == 0 {
			ev.PreferContexts = append([]string(nil), prefer...)
		}
	}
	for i := range req.Criteria {
		apply(req.Criteria[i].Metric, &req.Criteria[i].Evidence)
	}
	for i := range req.OrderBy {
		apply(req.OrderBy[i].Metric, &req.OrderBy[i].Evidence)
	}
	return nil
}

// queryColumn is one value column of the query table. Result entries align
// with the request's terms by position in every projection. Terms with the
// same metric and evidence policy resolve identically and share a column;
// one metric under different policies gets one column per policy.
type queryColumn struct {
	metric   string
	policy   query.EvidencePolicy
	label    string
	criteria []int
	order    []int
}

func queryColumns(req query.Request) []queryColumn {
	var columns []queryColumn
	add := func(metric string, policy query.EvidencePolicy) *queryColumn {
		for i := range columns {
			if columns[i].metric == metric && query.SameEvidencePolicy(columns[i].policy, policy) {
				return &columns[i]
			}
		}
		columns = append(columns, queryColumn{metric: metric, policy: policy})
		return &columns[len(columns)-1]
	}
	for i, cr := range req.Criteria {
		col := add(cr.Metric, cr.Evidence)
		col.criteria = append(col.criteria, i)
	}
	for k, o := range req.OrderBy {
		col := add(o.Metric, o.Evidence)
		col.order = append(col.order, k)
	}
	for i := range columns {
		columns[i].label = strings.ToUpper(columns[i].metric)
		for j := range columns {
			if j != i && columns[j].metric == columns[i].metric {
				columns[i].label += "[" + describePolicy(columns[i].policy) + "]"
				break
			}
		}
	}
	return columns
}

// value returns the column's resolution for one result. An order entry always
// carries it; a compact criterion reported through value_in_order_by carries
// no state, but then the column also has that order term.
func (col queryColumn) value(r query.Result) query.Resolution {
	if len(col.order) > 0 {
		return r.Order[col.order[0]].Resolution
	}
	for _, i := range col.criteria {
		if r.Criteria[i].State != "" {
			return r.Criteria[i].Resolution
		}
	}
	return query.Resolution{}
}

// describePolicy names an evidence policy compactly, to tell apart columns for
// one metric; the default policy is "default".
func describePolicy(p query.EvidencePolicy) string {
	var parts []string
	if len(p.Contexts) > 0 {
		parts = append(parts, "contexts="+strings.Join(p.Contexts, "|"))
	}
	if len(p.ContextKinds) > 0 {
		parts = append(parts, "kinds="+strings.Join(p.ContextKinds, "|"))
	}
	if len(p.PreferContexts) > 0 {
		parts = append(parts, "prefer="+strings.Join(p.PreferContexts, ">"))
	}
	if p.ExactEffort {
		parts = append(parts, "exact_effort")
	}
	if p.ExactServing {
		parts = append(parts, "exact_serving")
	}
	if len(parts) == 0 {
		return "default"
	}
	return strings.Join(parts, ",")
}

func formatResolution(r query.Resolution) (string, bool) {
	if r.State == query.StateAmbiguous {
		if r.ValueRange != nil {
			return formatValue(r.ValueRange.Min) + ".." + formatValue(r.ValueRange.Max) + "?", true
		}
		return "?", true
	}
	var proxy, effortExact, servingExact bool
	switch {
	case r.State == query.StateMissing:
		return "—", false
	case r.Selected != nil:
		proxy, effortExact, servingExact = r.Selected.Proxy, r.Selected.EffortExact, r.Selected.ServingExact
	case r.Evidence != nil:
		proxy, effortExact, servingExact = r.Evidence.Proxy, r.Evidence.EffortExact, r.Evidence.ServingExact
	case r.State == query.StateResolved:
		proxy = contains(r.Flags, query.FlagProxy)
		effortExact, servingExact = !contains(r.Flags, query.FlagEffortInexact), !contains(r.Flags, query.FlagServingInexact)
	default:
		return "—", false
	}
	s := formatValue(r.Value)
	flagged := false
	if proxy {
		s += "*"
		flagged = true
	}
	if !effortExact || !servingExact {
		s += "~"
		flagged = true
	}
	return s, flagged
}

func formatValue(v any) string {
	switch n := v.(type) {
	case float64:
		return strconv.FormatFloat(n, 'f', -1, 64)
	case int64:
		return strconv.FormatInt(n, 10)
	}
	return fmt.Sprint(v)
}

// ---- model ------------------------------------------------------------------

func (c *command) model(args []string) error {
	fs := c.flags("model")
	// This view is a local diagnostic, so it defaults to the full projection
	// and shows each value's own observation. The MCP tool defaults to compact
	// instead, where the response is charged for as context; --detail compact
	// shows exactly what an agent receives.
	detail := fs.String("detail", query.DetailFull, "full (default) or compact")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) == 0 {
		return usagef("usage: devmodels model <uid-or-label> [--detail compact|full] [--json]")
	}
	svc, err := c.service()
	if err != nil {
		return err
	}
	d, err := svc.ModelDetails(c.ctx, strings.Join(pos, " "), *detail)
	if err != nil {
		return err
	}
	if c.json {
		return c.writeJSON(d)
	}
	m := d.Model
	c.wrapped("", "  ", fmt.Sprintf("%s (%s)", m.Label, m.UID))
	c.printf("Provider: %s  Base: %s\n", m.Provider, m.BaseName)
	c.printf("Effort: %s  Serving: %s  Context variant: %s\n",
		dash(m.Effort), orStandard(m.ServingVariant), orStandard(m.ContextVariant))
	c.wrapped("Dataset: ", "  ", d.Dataset.DisplayName)
	c.printf("\n")
	// Both projections are rendered: full carries the selected observation and
	// every peer, compact carries the same provenance without the observation
	// objects, so only the source's own model name is unavailable there.
	for _, me := range d.Metrics {
		if me.State == query.StateAmbiguous {
			value, _ := formatResolution(me.Resolution)
			var srcs []string
			for _, p := range me.Peers {
				if !contains(srcs, p.Source) {
					srcs = append(srcs, p.Source)
				}
			}
			for _, p := range me.PeerValues {
				if p.Source != "" && !contains(srcs, p.Source) {
					srcs = append(srcs, p.Source)
				}
			}
			contexts := me.PeerContexts
			for _, p := range me.PeerValues {
				if !contains(contexts, p.Context) {
					contexts = append(contexts, p.Context)
				}
			}
			c.printf("  %s\n", me.Metric)
			c.wrapped("      ", "      ", strings.TrimSpace(value+" "+dash(me.Unit))+" — ambiguous")
			c.wrapped("      contexts: ", "        ", strings.Join(contexts, ", "))
			c.wrapped("      source: ", "        ", dash(strings.Join(srcs, ", ")))
			if me.AlternativeCount > 0 {
				c.printf("      %d alternative observation(s)\n", me.AlternativeCount)
			}
			continue
		}
		context, source, sourceModel := "-", "-", "-"
		var flags []string
		switch {
		case me.Selected != nil:
			sel := me.Selected
			context, source, sourceModel = sel.EvaluationContext, sel.Source, sel.SourceModelName
			flags = evidenceFlags(sel.Proxy, sel.EffortExact, sel.ServingExact, sel.Match == "alias")
		case me.Evidence != nil:
			ev := me.Evidence
			context, source = ev.Context, ev.Source
			flags = evidenceFlags(ev.Proxy, ev.EffortExact, ev.ServingExact, ev.Alias)
		}
		// The metric key can be nearly fifty columns on its own, so it takes a
		// line and its value and provenance are described beneath it.
		c.printf("  %s\n", me.Metric)
		c.wrapped("      ", "      ", strings.TrimSpace(formatValue(me.Value)+" "+dash(me.Unit)))
		provenance := context
		if source != "-" {
			provenance += " · " + source
		}
		if sourceModel != "-" && sourceModel != "" {
			provenance += " · " + sourceModel
		}
		c.wrapped("      ", "        ", provenance)
		if len(flags) > 0 || me.AlternativeCount > 0 {
			line := strings.Join(flags, ", ")
			if me.AlternativeCount > 0 {
				if line != "" {
					line += " · "
				}
				line += fmt.Sprintf("%d alternative observation(s)", me.AlternativeCount)
			}
			c.wrapped("      ", "        ", line)
		}
	}
	if len(d.MissingMetrics) > 0 {
		c.printf("\n")
		c.wrapped("No evidence: ", "  ", strings.Join(d.MissingMetrics, ", "))
	}
	for _, cv := range d.Caveats {
		c.wrapped("caveat: ", "  ", cv)
	}
	if len(d.Rejected) > 0 {
		c.printf("\nSimilarly named rows that carried no usable value:\n")
		for _, r := range d.Rejected {
			c.wrapped("  ", "    ", r.Source+": "+r.SourceModelName)
			c.wrapped("    ", "    ", r.Reason)
		}
	}
	return nil
}

// ---- unmatched, rejected and aliases ---------------------------------------

// unmatched lists evidence that was ingested correctly but describes a model
// identity the selected dataset does not offer. It is the identity-review
// surface: a row here may be a model Devin does not sell, or a name spelled
// differently enough that an alias is needed.
func (c *command) unmatched(args []string) error {
	fs := c.flags("unmatched")
	source := fs.String("source", "", "only this source")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	svc, err := c.service()
	if err != nil {
		return err
	}
	entries, err := svc.Unmatched(c.ctx, *source)
	if err != nil {
		return err
	}
	if c.json {
		return c.writeJSON(entries)
	}
	// Source model names and suggestion lists are both source-supplied and both
	// routinely long. The identifying columns take a fixed budget, with the
	// name truncated where it must be; the suggestions, which are the reason to
	// read this list at all, wrap onto their own line beneath. `--json` carries
	// every value whole.
	const (
		sourceCol  = 7
		nameCol    = 34
		metricCol  = 22
		contextCol = Width - sourceCol - nameCol - metricCol
	)
	c.printf("%s%s%s%s\n",
		pad("SOURCE", sourceCol), pad("SOURCE MODEL", nameCol),
		pad("METRIC", metricCol), "CONTEXT")
	for _, e := range entries {
		// The last column is not padded: trailing spaces are invisible width.
		c.printf("%s%s%s%s\n",
			pad(truncate(e.Source, sourceCol-1), sourceCol),
			pad(truncate(e.SourceModelName, nameCol-1), nameCol),
			pad(truncate(e.Metric, metricCol-1), metricCol),
			truncate(e.EvaluationContext, contextCol))
		if s := suggestions(e.Suggestions); s != "" && s != "-" {
			c.wrapped("    suggested: ", "      ", s)
		}
	}
	// A row is one model name, metric and context; several stored values can
	// share those, so the two counts differ and only one of them is the data
	// point count `devmodels status` reports.
	points := 0
	for _, e := range entries {
		points += e.DataPoints
	}
	c.printf("\n")
	c.wrapped("", "", fmt.Sprintf("%d rows above, covering %d data points that describe models this dataset does not offer. Most leaderboard rows do; confirm a genuine match with `devmodels alias add`.", len(entries), points))
	return nil
}

// rejected lists published rows this version could not represent faithfully. Unlike
// unmatched rows, these carry no value at all, so a growing count is worth
// investigating.
func (c *command) rejected(args []string) error {
	fs := c.flags("rejected")
	source := fs.String("source", "", "only this source")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	svc, err := c.service()
	if err != nil {
		return err
	}
	entries, err := svc.Rejected(c.ctx, *source)
	if err != nil {
		return err
	}
	if c.json {
		return c.writeJSON(entries)
	}
	if len(entries) == 0 {
		c.wrapped("", "", "No published row was rejected: every row of every source became evidence or was hidden by the source itself.")
		return nil
	}
	// The reason is the point of this listing and is written by the collector
	// to be read, so it is never truncated — it wraps under the row that earned
	// it.
	for _, e := range entries {
		c.wrapped("", "  ", fmt.Sprintf("%s  %s  %s", e.Source, e.SourceRowRef, e.SourceModelName))
		c.wrapped("    ", "    ", e.Reason)
	}
	return nil
}

func suggestions(s []query.Suggestion) string {
	var parts []string
	for _, x := range s {
		parts = append(parts, fmt.Sprintf("%s (%.2f)", x.Label, x.Similarity))
	}
	return dash(strings.Join(parts, "; "))
}

func (c *command) alias(args []string) error {
	fs := c.flags("alias")
	source := fs.String("source", "", "source code")
	name := fs.String("name", "", "model name exactly as the source publishes it")
	target := fs.String("target", "", "catalog identity as a Devin-style label")
	note := fs.String("note", "", "why the mapping is correct")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usagef("usage: devmodels alias add|list|remove [--source S --name NAME [--target LABEL] [--note TEXT]]")
	}
	svc, err := c.service()
	if err != nil {
		return err
	}
	switch pos[0] {
	case "list":
		aliases, err := svc.Aliases(c.ctx)
		if err != nil {
			return err
		}
		if c.json {
			return c.writeJSON(aliases)
		}
		w := c.table()
		fmt.Fprintln(w, "SOURCE\tSOURCE NAME\tTARGET\tNOTE")
		for _, a := range aliases {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", a.SourceCode, a.SourceName, a.TargetLabel, a.Note)
		}
		return w.Flush()
	case "add":
		if *source == "" || *name == "" || *target == "" {
			return usagef("alias add needs --source, --name and --target")
		}
		a, err := svc.AddAlias(c.ctx, *source, *name, *target, *note)
		if err != nil {
			return err
		}
		if c.json {
			return c.writeJSON(a)
		}
		c.wrapped("", "  ", fmt.Sprintf("%s rows named %q now resolve to %s", a.SourceCode, a.SourceName, a.TargetLabel))
		return nil
	case "remove":
		if *source == "" || *name == "" {
			return usagef("alias remove needs --source and --name")
		}
		if err := svc.RemoveAlias(c.ctx, *source, *name); err != nil {
			return err
		}
		if c.json {
			return c.writeJSON(map[string]any{"removed": true})
		}
		c.printf("removed\n")
		return nil
	}
	return usagef("unknown alias subcommand %q", pos[0])
}

// ---- import, browser, config, mcp -------------------------------------------

func (c *command) importCmd(args []string) error {
	fs := c.flags("import")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	switch {
	case len(pos) == 2 && pos[0] == "remove":
		svc, err := c.service()
		if err != nil {
			return err
		}
		if err := svc.RemoveImport(c.ctx, pos[1]); err != nil {
			return err
		}
		if c.json {
			return c.writeJSON(map[string]any{"removed": pos[1]})
		}
		c.printf("removed manual source %s\n", pos[1])
		return nil
	case len(pos) == 1:
		raw, err := os.ReadFile(pos[0])
		if err != nil {
			return err
		}
		svc, err := c.service()
		if err != nil {
			return err
		}
		rep, err := svc.Import(c.ctx, raw)
		if err != nil {
			return err
		}
		if c.json {
			return c.writeJSON(rep)
		}
		c.printf("imported %s: %d data points\n", rep.Source, rep.Counts.DataPoints)
		return nil
	}
	return usagef("usage: devmodels import <file.json> | devmodels import remove <code>")
}

func (c *command) browser(args []string) error {
	fs := c.flags("browser")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 || (pos[0] != "status" && pos[0] != "install" && pos[0] != "check") {
		return usagef("usage: devmodels browser status|install|check [--json]")
	}
	svc, err := c.service()
	if err != nil {
		return err
	}
	if pos[0] == "check" {
		res, err := svc.BrowserCheck(c.ctx)
		if c.json && res != nil {
			if jerr := c.writeJSON(res); jerr != nil {
				return jerr
			}
			if err != nil {
				c.json = false
			}
		} else if err == nil {
			c.printf("browser runtime ok in %dms\n", res.DurationMS)
			c.printf("  navigated=%v clicked_and_waited=%v evaluated=%v\n", res.Navigated, res.Interacted, res.Evaluated)
			c.wrapped("executable: ", "  ", res.Runtime.Executable)
			c.wrapped("user agent: ", "  ", res.UserAgent)
		}
		return err
	}
	if pos[0] == "status" {
		st, err := svc.BrowserStatus()
		if err != nil {
			return err
		}
		if c.json {
			return c.writeJSON(st)
		}
		platform := st.Platform
		if platform == "" {
			platform = "none published for this platform"
		}
		c.wrapped("", "  ", st.Detail)
		c.printf("Platform: %s\n", platform)
		c.printf("Pinned Chrome for Testing: %s\n", st.PinnedVersion)
		c.wrapped("Directory: ", "  ", st.Root)
		if st.Executable != "" {
			c.wrapped("Executable: ", "  ", st.Executable)
		}
		return nil
	}
	st, err := svc.BrowserInstall(c.ctx)
	if err != nil {
		return err
	}
	if c.json {
		return c.writeJSON(st)
	}
	c.wrapped("", "  ", st.Detail)
	c.wrapped("  ", "  ", st.Executable)
	return nil
}

func (c *command) config(args []string) error {
	fs := c.flags("config")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	cfg, err := c.resolved()
	if err != nil {
		return err
	}
	switch {
	case len(pos) == 0 || (len(pos) == 1 && pos[0] == "show"):
		if c.json {
			return c.writeJSON(cfg)
		}
		// Every value here is a path. Each gets a labelled line of its own and
		// wraps rather than being truncated, because a partial path is useless.
		c.wrapped("Config file:     ", "    ", cfg.ConfigFile)
		c.wrapped("Database:        ", "    ", cfg.DBPath)
		c.printf("    (%s)\n", cfg.DBPathSource)
		c.wrapped("Data dir:        ", "    ", cfg.Paths.DataDir)
		c.wrapped("Cache dir:       ", "    ", cfg.Paths.CacheDir)
		c.wrapped("Browser runtime: ", "    ", cfg.BrowserDir)
		return nil
	case len(pos) == 3 && pos[0] == "set" && pos[1] == "db-path":
		file, err := config.ReadFile(cfg.ConfigFile)
		if err != nil {
			return err
		}
		file.DBPath = absolute(pos[2])
		if err := config.WriteFile(cfg.ConfigFile, file); err != nil {
			return err
		}
		c.wrapped("db_path set to ", "  ", file.DBPath)
		c.wrapped("in ", "  ", cfg.ConfigFile)
		return nil
	case len(pos) == 2 && pos[0] == "unset" && pos[1] == "db-path":
		file, err := config.ReadFile(cfg.ConfigFile)
		if err != nil {
			return err
		}
		file.DBPath = ""
		if err := config.WriteFile(cfg.ConfigFile, file); err != nil {
			return err
		}
		c.wrapped("db_path removed from ", "  ", cfg.ConfigFile)
		return nil
	}
	return usagef("usage: devmodels config [show] | config set db-path PATH | config unset db-path")
}

func (c *command) mcp(args []string) error {
	fs := c.flags("mcp")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	// JSON output makes no sense here: stdout carries the protocol.
	c.json = false
	svc, err := c.service()
	if err != nil {
		return err
	}
	return mcpserver.Serve(c.ctx, svc)
}

// ---- helpers ----------------------------------------------------------------

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// optionalCount renders a count that does not apply to every source, so a
// column that is blank for one source is visibly not-applicable rather than
// zero.
func optionalCount(n *int) string {
	if n == nil {
		return "-"
	}
	return strconv.Itoa(*n)
}

// evidenceFlags names the ways a value is weaker evidence, in the order the
// text output has always listed them. No flags means the value is
// Devin-measured or Devin-published and exact for this variant.
func evidenceFlags(proxy, effortExact, servingExact, alias bool) []string {
	var flags []string
	if proxy {
		flags = append(flags, "proxy")
	}
	if !effortExact {
		flags = append(flags, "effort-inexact")
	}
	if !servingExact {
		flags = append(flags, "serving-inexact")
	}
	if alias {
		flags = append(flags, "alias")
	}
	return flags
}

func orStandard(s string) string {
	if s == "" {
		return "standard"
	}
	return s
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func absolute(p string) string {
	if p == ":memory:" {
		return p
	}
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return p
}
