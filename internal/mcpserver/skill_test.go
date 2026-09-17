package mcpserver_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/Tim-Butterfield/devin-model-catalog/internal/app"
	"github.com/Tim-Butterfield/devin-model-catalog/internal/devin"
	"github.com/Tim-Butterfield/devin-model-catalog/internal/query"
	"github.com/Tim-Butterfield/devin-model-catalog/internal/sources/bfcl"
	"github.com/Tim-Butterfield/devin-model-catalog/internal/sources/epoch"
)

// The shipped Devin skill tells an agent which tools to call, which metric keys
// to ask for and how to shape a request. Nothing checks that at runtime: a
// renamed tool or a retired metric key would simply make the skill ask for
// something that no longer exists, and the failure would land on a user in a
// session rather than here.
//
// So the vocabulary is checked against the server rather than against a copy of
// it. Every identifier-shaped name the skill uses has to be a real tool name, a
// real metric key, a real field of the request or response, or a real evidence
// flag; every request it shows has to be one the query schema accepts; and no
// model, price or score may be written into it, because the skill's whole
// premise is that those come from a tool result.
//
// This deliberately does not parse the skill's prose. It reads the names, which
// is where drift actually shows up.

const skillPath = "../../devin/skills/recommend-model/SKILL.md"

func skillText(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("the skill this suite exists to check is missing: %v", err)
	}
	return string(raw)
}

// backtickedNames returns every single-backtick span in the document whose
// whole content is one lowercase identifier, splitting them by shape: a name
// with an underscore in it is a tool, a metric key or a field, and has to be
// one of those exactly; a bare word may also be a value the wire contract
// accepts, such as an operator or a direction.
func backtickedNames(doc string) (identifiers, words []string) {
	span := regexp.MustCompile("`([^`\n]+)`")
	ident := regexp.MustCompile(`^[a-z][a-z0-9]*(_[a-z0-9]+)+$`)
	word := regexp.MustCompile(`^[a-z][a-z0-9]{1,}$`)
	seen := map[string]bool{}
	for _, m := range span.FindAllStringSubmatch(doc, -1) {
		name := m[1]
		if seen[name] {
			continue
		}
		switch {
		case ident.MatchString(name):
			seen[name] = true
			identifiers = append(identifiers, name)
		case word.MatchString(name):
			seen[name] = true
			words = append(words, name)
		}
	}
	slices.Sort(identifiers)
	slices.Sort(words)
	return identifiers, words
}

// wireContractWords harvests the values the request schema's own field
// descriptions name — the operators, directions, serving variants and
// projections an agent is allowed to write. Taking them from the tags means a
// renamed operator is caught here rather than rediscovered in a session.
func wireContractWords(t reflect.Type, into map[string]bool, visited map[reflect.Type]bool) {
	for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice || t.Kind() == reflect.Array {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct || visited[t] {
		return
	}
	visited[t] = true
	token := regexp.MustCompile(`[a-z][a-z0-9_]+`)
	for i := range t.NumField() {
		f := t.Field(i)
		for _, w := range token.FindAllString(f.Tag.Get("jsonschema"), -1) {
			into[w] = true
		}
		wireContractWords(f.Type, into, visited)
	}
}

// jsonFieldNames collects the JSON field names reachable from a type, which is
// where the request and response vocabulary actually lives.
func jsonFieldNames(t reflect.Type, names map[string]bool, visited map[reflect.Type]bool) {
	for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice || t.Kind() == reflect.Array {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct || visited[t] {
		return
	}
	visited[t] = true
	for i := range t.NumField() {
		f := t.Field(i)
		if name, _, _ := strings.Cut(f.Tag.Get("json"), ","); name != "" && name != "-" {
			names[name] = true
		}
		jsonFieldNames(f.Type, names, visited)
	}
}

// liveVocabulary is every name the skill may use, taken from the running
// server, the collectors, and the request and response types.
func liveVocabulary(t *testing.T) map[string]string {
	t.Helper()
	vocab := map[string]string{}

	s := connect(t, service(t, false))
	tools, err := s.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools.Tools {
		vocab[tool.Name] = "MCP tool"
	}

	defs := devin.MetricDefinitions()
	defs = append(defs, epoch.New().Metrics()...)
	defs = append(defs, bfcl.New().Metrics()...)
	for _, def := range defs {
		vocab[def.Key] = "metric"
	}

	names := map[string]bool{}
	visited := map[reflect.Type]bool{}
	for _, v := range []any{
		query.Request{}, query.Response{}, query.Result{},
		query.Details{}, query.Description{}, app.Status{},
	} {
		jsonFieldNames(reflect.TypeOf(v), names, visited)
	}
	for name := range names {
		if _, taken := vocab[name]; !taken {
			vocab[name] = "request or response field"
		}
	}

	for _, v := range []string{
		query.FlagProxy, query.FlagEffortInexact, query.FlagServingInexact, query.FlagAlias,
		query.StateResolved, query.StateMissing, query.StateAmbiguous,
		query.AmbiguityPeerContexts, query.AmbiguityConflictingValues,
	} {
		if _, taken := vocab[v]; !taken {
			vocab[v] = "evidence vocabulary"
		}
	}
	return vocab
}

// TestTheSkillOnlyNamesThingsTheServerHas is the check that matters: the skill
// may not name a tool, metric, field or value that does not exist.
func TestTheSkillOnlyNamesThingsTheServerHas(t *testing.T) {
	vocab := liveVocabulary(t)
	identifiers, words := backtickedNames(skillText(t))
	if len(identifiers) < 20 {
		t.Fatalf("only %d identifiers found in the skill; the extraction is probably broken: %v",
			len(identifiers), identifiers)
	}
	for _, name := range identifiers {
		kind, ok := vocab[name]
		if !ok {
			t.Errorf("the skill names %q, which the server does not have", name)
			continue
		}
		t.Logf("%-46s %s", name, kind)
	}

	accepted := map[string]bool{}
	visited := map[reflect.Type]bool{}
	wireContractWords(reflect.TypeOf(query.Request{}), accepted, visited)
	// The program's own name is not part of the wire contract.
	accepted["devmodels"] = true
	for _, name := range words {
		if _, ok := vocab[name]; ok {
			continue
		}
		if !accepted[name] {
			t.Errorf("the skill uses %q, which is neither a name the server has "+
				"nor a value the request schema accepts", name)
		}
	}
}

// TestTheSkillNamesEveryToolItReliesOn is the other direction: a tool the skill
// is written around must still be there, under that name.
func TestTheSkillNamesEveryToolItReliesOn(t *testing.T) {
	doc := skillText(t)
	vocab := liveVocabulary(t)
	for _, tool := range []string{
		"query_models", "data_status", "describe_available_data", "get_model_details",
	} {
		if !strings.Contains(doc, "`"+tool+"`") {
			t.Errorf("the skill no longer mentions %s", tool)
		}
		if vocab[tool] != "MCP tool" {
			t.Errorf("%s is not a tool on the server any more", tool)
		}
	}
}

// TestTheSkillsExampleRequestsAreValid runs every JSON block in the skill
// through the type the tool takes, refusing unknown fields — so a field
// invented in an example fails here rather than in someone's session.
func TestTheSkillsExampleRequestsAreValid(t *testing.T) {
	blocks := regexp.MustCompile("(?s)```json\n(.*?)```").FindAllStringSubmatch(skillText(t), -1)
	if len(blocks) == 0 {
		t.Fatal("the skill shows no request at all; an agent has nothing to copy")
	}
	for i, b := range blocks {
		dec := json.NewDecoder(strings.NewReader(b[1]))
		dec.DisallowUnknownFields()
		var req query.Request
		if err := dec.Decode(&req); err != nil {
			t.Errorf("example %d is not a valid query request: %v\n%s", i+1, err, b[1])
		}
	}
}

// TestTheSkillStatesNoCurrentFacts guards the architecture boundary. Prices,
// scores and model names belong in a tool result and never in the skill: a
// figure written here would be wrong by the next refresh, and an agent reading
// it would have no way to tell.
func TestTheSkillStatesNoCurrentFacts(t *testing.T) {
	doc := skillText(t)
	// The structural example carries placeholder zeroes, so measure the prose.
	prose := regexp.MustCompile("(?s)```.*?```").ReplaceAllString(doc, "")
	if m := regexp.MustCompile(`\d+\.\d+`).FindAllString(prose, -1); len(m) > 0 {
		t.Errorf("the skill states decimal figures, which is how a price or a score gets frozen into it: %v", m)
	}

	// No model from a loaded catalog may appear in it. Uids and labels carrying
	// a digit are distinctive enough that a match is a real one.
	svc := service(t, true)
	resp, err := svc.Query(context.Background(), query.Request{Limit: 500})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Results) == 0 {
		t.Fatal("the fixture catalog is empty, so this check proves nothing")
	}
	digit := regexp.MustCompile(`\d`)
	for _, r := range resp.Results {
		if strings.Contains(doc, r.Model.UID) {
			t.Errorf("the skill names the model uid %q", r.Model.UID)
		}
		if digit.MatchString(r.Model.Label) && strings.Contains(doc, r.Model.Label) {
			t.Errorf("the skill names the model %q", r.Model.Label)
		}
	}
	t.Logf("checked the skill against %d catalog models", len(resp.Results))
}

// TestTheSkillIsAWellFormedDevinSkill checks the parts Devin itself reads: the
// frontmatter block, the name matching the directory that identifies the skill,
// and a description for the skill list.
func TestTheSkillIsAWellFormedDevinSkill(t *testing.T) {
	doc := skillText(t)
	if !strings.HasPrefix(doc, "---\n") {
		t.Fatal("no YAML frontmatter; Devin reads name, description and allowed-tools from it")
	}
	end := strings.Index(doc[4:], "\n---\n")
	if end < 0 {
		t.Fatal("the frontmatter block is not closed")
	}
	front := doc[4 : 4+end]

	dir := filepath.Base(filepath.Dir(skillPath))
	if !strings.Contains(front, "name: "+dir) {
		t.Errorf("frontmatter name must match the directory %q, which identifies the skill:\n%s", dir, front)
	}
	for _, field := range []string{"description:", "argument-hint:"} {
		if !strings.Contains(front, field) {
			t.Errorf("frontmatter has no %s", field)
		}
	}
	if strings.Contains(doc, "\r") {
		t.Error("the skill carries carriage returns; it is read as a prompt on every platform")
	}
	if !strings.Contains(doc, "$ARGUMENTS") {
		t.Error("the skill never uses $ARGUMENTS, so an invocation argument would be appended rather than placed")
	}
}
