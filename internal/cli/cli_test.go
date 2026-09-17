package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	devmodelcatalog "github.com/Tim-Butterfield/devin-model-catalog"
	"github.com/Tim-Butterfield/devin-model-catalog/internal/app"
	"github.com/Tim-Butterfield/devin-model-catalog/internal/cli"
	"github.com/Tim-Butterfield/devin-model-catalog/internal/devin"
	"github.com/Tim-Butterfield/devin-model-catalog/internal/query"
	"github.com/Tim-Butterfield/devin-model-catalog/internal/retrieval"
	"github.com/Tim-Butterfield/devin-model-catalog/internal/sources/bfcl"
	"github.com/Tim-Butterfield/devin-model-catalog/internal/sources/epoch"
	"github.com/Tim-Butterfield/devin-model-catalog/internal/testfixtures"
)

type runner struct {
	t     *testing.T
	vars  map[string]string
	fetch *retrieval.Static
}

func newRunner(t *testing.T) *runner {
	dir := t.TempDir()
	return &runner{
		t: t,
		vars: map[string]string{
			"DEVMODELS_DB":     filepath.Join(dir, "devmodels.db"),
			"DEVMODELS_CONFIG": filepath.Join(dir, "config.json"),
			"HOME":             dir,
		},
		fetch: &retrieval.Static{Bodies: map[string][]byte{
			devin.ModelsURL:  []byte(testfixtures.DevinPage()),
			epoch.ArchiveURL: testfixtures.DefaultEpochArchive(),
			bfcl.DataURL:     []byte(testfixtures.BFCLCSV),
		}, Errors: map[string]error{}},
	}
}

func (r *runner) run(args ...string) (int, string, string) {
	var stdout, stderr bytes.Buffer
	code := cli.Run(context.Background(), args, cli.Env{
		Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr,
		Getenv:    func(k string) string { return r.vars[k] },
		Configure: func(o *app.Options) { o.HTTP = r.fetch },
	})
	return code, stdout.String(), stderr.String()
}

func (r *runner) must(args ...string) string {
	r.t.Helper()
	code, out, errOut := r.run(args...)
	if code != 0 {
		r.t.Fatalf("devmodels %s: exit %d\nstdout: %s\nstderr: %s", strings.Join(args, " "), code, out, errOut)
	}
	return out
}

func TestAgentsMDIsTheEmbeddedFileExactly(t *testing.T) {
	onDisk, err := os.ReadFile("../../AGENTS.md")
	if err != nil {
		t.Fatal(err)
	}
	if devmodelcatalog.AgentsMD != string(onDisk) {
		t.Fatal("embedded AGENTS.md differs from the repository file")
	}
	out := newRunner(t).must("agents-md")
	if out != string(onDisk) {
		t.Fatal("`devmodels agents-md` must print AGENTS.md byte for byte")
	}
}

func TestWorkflowJSONAndSharedService(t *testing.T) {
	r := newRunner(t)

	var list app.DatasetList
	if err := json.Unmarshal([]byte(r.must("datasets", "refresh", "--json")), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Datasets) != 3 || list.Datasets[1].DisplayName != "Enterprise (ACUs)" {
		t.Fatalf("datasets: %+v", list.Datasets)
	}
	text := r.must("datasets")
	if !strings.Contains(text, "Legacy enterprise (credits)") || strings.Contains(text, "no:") {
		t.Fatalf("datasets text:\n%s", text)
	}

	r.must("use-dataset", "2")

	var report app.RefreshReport
	if err := json.Unmarshal([]byte(r.must("refresh", "--json")), &report); err != nil {
		t.Fatal(err)
	}
	if !report.Succeeded || len(report.Sources) != 3 {
		t.Fatalf("refresh report: %+v", report)
	}

	cliOut := r.must("query", "--where", "swe_bench_verified>=77", "--order", "devin_output_price_usd_per_mtok", "--json")

	// The same request through the service must give the same answer.
	svc, err := app.Open(context.Background(), app.Options{DBPath: r.vars["DEVMODELS_DB"], HTTP: r.fetch})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	direct, err := svc.Query(context.Background(), query.Request{
		Criteria: []query.Criterion{{Metric: "swe_bench_verified", Op: "gte", Value: 77.0, Missing: query.MissingReject}},
		OrderBy:  []query.OrderTerm{{Metric: "devin_output_price_usd_per_mtok"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	directJSON, _ := json.Marshal(direct)
	var a, b any
	json.Unmarshal([]byte(cliOut), &a)
	json.Unmarshal(directJSON, &b)
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("CLI and service disagree:\ncli: %s\nsvc: %s", cliOut, directJSON)
	}

	for _, detail := range []string{query.DetailSummary, query.DetailFull} {
		cliOut := r.must("query", "--where", "swe_bench_verified>=77", "--order", "devin_output_price_usd_per_mtok", "--detail", detail, "--json")
		direct, err := svc.Query(context.Background(), query.Request{
			Criteria: []query.Criterion{{Metric: "swe_bench_verified", Op: "gte", Value: 77.0, Missing: query.MissingReject}},
			OrderBy:  []query.OrderTerm{{Metric: "devin_output_price_usd_per_mtok"}},
			Detail:   detail,
		})
		if err != nil {
			t.Fatal(err)
		}
		directJSON, _ := json.Marshal(direct)
		var a, b any
		json.Unmarshal([]byte(cliOut), &a)
		json.Unmarshal(directJSON, &b)
		if !reflect.DeepEqual(a, b) {
			t.Fatalf("CLI and service disagree for detail %s", detail)
		}
	}

	// Human-readable output renders every projection the same way.
	for _, detail := range []string{"", query.DetailSummary, query.DetailFull} {
		args := []string{"query", "--where", "swe_bench_verified>=77", "--order", "devin_output_price_usd_per_mtok"}
		if detail != "" {
			args = append(args, "--detail", detail)
		}
		text = r.must(args...)
		if !strings.Contains(text, "claude-opus-5-medium") || !strings.Contains(text, "78.1*") {
			t.Fatalf("query text for detail %q:\n%s", detail, text)
		}
	}
	for _, bad := range [][]string{{"query", "--detail", "compact", "--alternatives"}, {"query", "--detail", "tiny"}} {
		if code, _, _ := r.run(bad...); code != cli.ExitUsage {
			t.Fatalf("%v: exit %d", bad, code)
		}
	}
	if out := r.must("model", "claude-opus-5-medium"); !strings.Contains(out, "swe_bench_verified") {
		t.Fatalf("model text:\n%s", out)
	}
	if out := r.must("status"); !strings.Contains(out, "Usable: yes") {
		t.Fatalf("status text:\n%s", out)
	}
	if out := r.must("metrics"); strings.Contains(out, "access basis:") || !strings.Contains(out, "--detail full") {
		t.Fatalf("metrics summary text:\n%s", out)
	}
	if out := r.must("metrics", "--detail", "full"); !strings.Contains(out, "access basis:") {
		t.Fatalf("metrics full text:\n%s", out)
	}
	for _, detail := range []string{"", "full"} {
		args := []string{"metrics", "--json"}
		if detail != "" {
			args = append(args, "--detail", detail)
		}
		cliDescribe := r.must(args...)
		svcDescribe, err := svc.Describe(context.Background(), detail)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(svcDescribe)
		var x, y any
		json.Unmarshal([]byte(cliDescribe), &x)
		json.Unmarshal(raw, &y)
		if !reflect.DeepEqual(x, y) {
			t.Fatalf("CLI and service describe differ for detail %q", detail)
		}
	}
	if code, _, _ := r.run("metrics", "--detail", "verbose"); code != cli.ExitUsage {
		t.Fatalf("invalid metrics detail: exit %d", code)
	}
	// An unfamiliar harness is evidence, not a rejection, so it belongs in the
	// unmatched review surface rather than being listed as a bad row.
	if out := r.must("rejected"); strings.Contains(out, "Droid") {
		t.Fatalf("an unfamiliar harness must not be reported as a rejected row:\n%s", out)
	}
	if out := r.must("unmatched"); !strings.Contains(out, "SOURCE MODEL") {
		t.Fatalf("unmatched text:\n%s", out)
	}
}

func TestExitCodes(t *testing.T) {
	r := newRunner(t)
	cases := []struct {
		args []string
		code int
	}{
		{[]string{"frobnicate"}, cli.ExitUsage},
		{[]string{}, cli.ExitUsage},
		{[]string{"query"}, cli.ExitNotReady},
		{[]string{"use-dataset", "1"}, cli.ExitNotReady},
		{[]string{"use-dataset", "x"}, cli.ExitUsage},
		{[]string{"query", "--where", "swe_bench_verified>>1"}, cli.ExitUsage},
	}
	for _, c := range cases {
		if code, _, stderr := r.run(c.args...); code != c.code {
			t.Errorf("%v: exit %d want %d (%s)", c.args, code, c.code, stderr)
		}
	}

	r.must("datasets", "refresh")
	if code, _, _ := r.run("use-dataset", "9"); code != cli.ExitUsage {
		t.Errorf("unknown dataset id: exit %d", code)
	}
	r.must("use-dataset", "1")
	r.fetch.Errors[devin.ModelsURL] = os.ErrDeadlineExceeded
	code, out, _ := r.run("refresh", "--json")
	if code != cli.ExitSource {
		t.Errorf("source failure: exit %d", code)
	}
	var report app.RefreshReport
	if err := json.Unmarshal([]byte(out), &report); err != nil || report.Succeeded {
		t.Errorf("refresh --json on failure must still emit the report: %v %s", err, out)
	}

	code, out, _ = r.run("query", "--where", "nope>1", "--json")
	if code != cli.ExitUsage || !strings.Contains(out, `"exit_code": 2`) {
		t.Errorf("JSON error document: %d %s", code, out)
	}
}

func TestConfigurableDatabasePath(t *testing.T) {
	r := newRunner(t)
	delete(r.vars, "DEVMODELS_DB")
	dir := t.TempDir()
	fromConfig := filepath.Join(dir, "configured.db")

	r.must("config", "set", "db-path", fromConfig)
	var cfg struct {
		DBPath       string `json:"db_path"`
		DBPathSource string `json:"db_path_source"`
	}
	json.Unmarshal([]byte(r.must("config", "--json")), &cfg)
	if cfg.DBPath != fromConfig || cfg.DBPathSource != "config file" {
		t.Fatalf("config: %+v", cfg)
	}

	r.must("datasets", "refresh")
	if _, err := os.Stat(fromConfig); err != nil {
		t.Fatalf("the configured database was not used: %v", err)
	}

	flagDB := filepath.Join(dir, "flag.db")
	r.must("datasets", "refresh", "--db", flagDB)
	if _, err := os.Stat(flagDB); err != nil {
		t.Fatalf("--db was not used: %v", err)
	}

	r.must("config", "unset", "db-path")
	json.Unmarshal([]byte(r.must("config", "--json")), &cfg)
	if cfg.DBPathSource != "default" {
		t.Fatalf("after unset: %+v", cfg)
	}
}
