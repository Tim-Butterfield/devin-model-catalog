package mcpserver_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	devmodelcatalog "github.com/Tim-Butterfield/devin-model-catalog"
	"github.com/Tim-Butterfield/devin-model-catalog/internal/app"
	"github.com/Tim-Butterfield/devin-model-catalog/internal/devin"
	"github.com/Tim-Butterfield/devin-model-catalog/internal/mcpserver"
	"github.com/Tim-Butterfield/devin-model-catalog/internal/query"
	"github.com/Tim-Butterfield/devin-model-catalog/internal/retrieval"
	"github.com/Tim-Butterfield/devin-model-catalog/internal/sources/bfcl"
	"github.com/Tim-Butterfield/devin-model-catalog/internal/sources/epoch"
	"github.com/Tim-Butterfield/devin-model-catalog/internal/testfixtures"
)

func connect(t *testing.T, svc *app.Service) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	clientT, serverT := mcp.NewInMemoryTransports()
	if _, err := mcpserver.New(svc).Connect(ctx, serverT, nil); err != nil {
		t.Fatal(err)
	}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "v0"}, nil).Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}

func service(t *testing.T, refreshed bool) *app.Service {
	t.Helper()
	ctx := context.Background()
	fetch := &retrieval.Static{Bodies: map[string][]byte{
		devin.ModelsURL:  []byte(testfixtures.DevinPage()),
		epoch.ArchiveURL: testfixtures.DefaultEpochArchive(),
		bfcl.DataURL:     []byte(testfixtures.BFCLCSV),
	}}
	svc, err := app.Open(ctx, app.Options{DBPath: filepath.Join(t.TempDir(), "devmodels.db"), HTTP: fetch})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { svc.Close() })
	if refreshed {
		if _, err := svc.RefreshDatasets(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.UseDataset(ctx, 2); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.Refresh(ctx, app.RefreshOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	return svc
}

func call(t *testing.T, s *mcp.ClientSession, name string, args any) *mcp.CallToolResult {
	t.Helper()
	res, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: protocol error %v", name, err)
	}
	return res
}

func text(res *mcp.CallToolResult) string {
	var sb strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	return sb.String()
}

func TestToolSurface(t *testing.T) {
	s := connect(t, service(t, false))
	tools, err := s.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	want := []string{"agents_md", "data_status", "describe_available_data", "get_model_details", "query_models"}
	if !slices.Equal(names, want) {
		t.Fatalf("tools = %v", names)
	}
}

// Clients download the tool list on every connection, before they know
// whether they will use the catalog, so it has to stay small. This asserts two
// things: that the evidence policy is sent once rather than inlined under both
// criteria and order_by, and that the whole list stays within a generous
// ceiling. The ceiling is a regression guard, not a target — it is far above
// the current size and well below what it replaced.
func TestToolDeclarationsStayCompact(t *testing.T) {
	s := connect(t, service(t, false))
	tools, err := s.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(tools.Tools)
	if err != nil {
		t.Fatal(err)
	}
	// Sized just above what the declarations currently cost, and well below
	// the ~6.5 KB they cost before the evidence policy was hoisted and the
	// prose moved out. Adding bytes here should be a deliberate decision, not
	// a drift, so the headroom is small on purpose.
	const ceiling = 5600
	if len(encoded) > ceiling {
		t.Errorf("tool declarations are %d bytes, above the %d-byte ceiling; move guidance into AGENTS.md or describe_available_data", len(encoded), ceiling)
	}

	var byName map[string]any
	for _, tool := range tools.Tools {
		if tool.Name != "query_models" {
			continue
		}
		raw, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &byName); err != nil {
			t.Fatal(err)
		}
		if n := strings.Count(string(raw), `"exact_effort"`); n != 1 {
			t.Errorf("the evidence policy is described %d times; it must be sent once under $defs", n)
		}
		if !strings.Contains(string(raw), `"$ref":"#/$defs/evidence_policy"`) {
			t.Errorf("criteria and order_by must refer to the shared evidence policy: %s", raw)
		}
	}
	if byName == nil {
		t.Fatal("query_models is missing from the tool list")
	}
	// The de-duplication must not cost the request contract: every field of
	// the Go request type is still declared, so a client can still build one.
	props, _ := byName["properties"].(map[string]any)
	for _, field := range []string{"criteria", "order_by", "filter", "limit", "detail", "include_alternatives"} {
		if _, ok := props[field]; !ok {
			t.Errorf("input schema no longer declares %q", field)
		}
	}
}

// The hoisted schema must stay a faithful projection of query.Request: what it
// accepts and rejects has to match what the inferred schema would.
func TestDeDuplicatedSchemaAcceptsTheSameRequests(t *testing.T) {
	svc := service(t, true)
	s := connect(t, svc)

	valid := map[string]any{
		"criteria": []any{map[string]any{
			"metric": "swe_bench_verified", "op": "gte", "value": 77, "missing": "reject",
			"evidence": map[string]any{"contexts": []any{"epoch_ai_eval"}, "exact_effort": true},
		}},
		"order_by": []any{map[string]any{
			"metric": "swe_bench_verified", "direction": "desc",
			"evidence": map[string]any{"prefer_contexts": []any{"epoch_ai_eval"}, "exact_serving": true},
		}},
		"filter": map[string]any{"providers": []any{"ANTHROPIC"}},
		"limit":  5,
	}
	if res := call(t, s, "query_models", valid); res.IsError {
		t.Fatalf("a request using the evidence policy on both terms must be accepted: %s", text(res))
	}
	// The policy is still validated through the reference, not waved through.
	invalid := map[string]any{
		"criteria": []any{map[string]any{
			"metric": "swe_bench_verified", "op": "gte", "value": 77, "missing": "reject",
			"evidence": map[string]any{"no_such_field": true},
		}},
	}
	if res := call(t, s, "query_models", invalid); !res.IsError {
		t.Fatal("an unknown field inside the referenced evidence policy must still be rejected")
	}
}

func TestAgentsMDToolReturnsTheEmbeddedFile(t *testing.T) {
	s := connect(t, service(t, false))
	res := call(t, s, "agents_md", map[string]any{})
	if res.IsError || text(res) != devmodelcatalog.AgentsMD {
		t.Fatal("agents_md must return AGENTS.md exactly")
	}
}

func TestQueryMatchesTheService(t *testing.T) {
	svc := service(t, true)
	s := connect(t, svc)

	args := map[string]any{
		"criteria": []any{map[string]any{"metric": "swe_bench_verified", "op": "gte", "value": 77, "missing": "reject"}},
		"order_by": []any{map[string]any{"metric": "devin_output_price_usd_per_mtok"}},
	}
	req := query.Request{
		Criteria: []query.Criterion{{Metric: "swe_bench_verified", Op: "gte", Value: 77.0, Missing: query.MissingReject}},
		OrderBy:  []query.OrderTerm{{Metric: "devin_output_price_usd_per_mtok"}},
	}
	// Every projection, including the default, matches the service.
	for _, detail := range []string{"", query.DetailCompact, query.DetailSummary, query.DetailFull} {
		args["detail"], req.Detail = detail, detail
		res := call(t, s, "query_models", args)
		if res.IsError {
			t.Fatalf("query_models detail %q failed: %s", detail, text(res))
		}
		direct, err := svc.Query(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		var viaMCP, viaService any
		raw, _ := json.Marshal(res.StructuredContent)
		json.Unmarshal(raw, &viaMCP)
		raw, _ = json.Marshal(direct)
		json.Unmarshal(raw, &viaService)
		if !reflect.DeepEqual(viaMCP, viaService) {
			t.Fatalf("MCP and service disagree for detail %q:\nmcp: %v\nsvc: %v", detail, viaMCP, viaService)
		}
		out := text(res)
		compact := detail == "" || detail == query.DetailCompact
		if compact != strings.Contains(out, `"order_by":[`) || (detail == query.DetailSummary) != strings.Contains(out, `"evidence":{"source"`) ||
			(detail == query.DetailFull) != strings.Contains(out, `"selected"`) {
			t.Fatalf("detail %q has the wrong shape: %s", detail, out)
		}
	}
	bad := call(t, s, "query_models", map[string]any{"detail": "compact", "include_alternatives": true})
	if !bad.IsError || !strings.Contains(text(bad), "include_alternatives") {
		t.Fatalf("compact with alternatives must be a tool error: %s", text(bad))
	}

	// Both detail projections match the service, and the default is the
	// compact one: an agent gets the provenance it needs to explain a value
	// without the observation objects behind it.
	for _, detail := range []string{"", query.DetailCompact, query.DetailFull} {
		args := map[string]any{"model": "claude-opus-5-medium"}
		if detail != "" {
			args["detail"] = detail
		}
		details := call(t, s, "get_model_details", args)
		if details.IsError {
			t.Fatalf("get_model_details detail %q: %s", detail, text(details))
		}
		direct, err := svc.ModelDetails(context.Background(), "claude-opus-5-medium", detail)
		if err != nil {
			t.Fatal(err)
		}
		var viaMCP, viaService any
		raw, _ := json.Marshal(details.StructuredContent)
		json.Unmarshal(raw, &viaMCP)
		raw, _ = json.Marshal(direct)
		json.Unmarshal(raw, &viaService)
		if !reflect.DeepEqual(viaMCP, viaService) {
			t.Fatalf("MCP and service details disagree for detail %q", detail)
		}
		out := text(details)
		if !strings.Contains(out, "swe_bench_verified") || !strings.Contains(out, `"caveats"`) || !strings.Contains(out, `"evaluation_context"`) {
			t.Fatalf("every projection carries the metrics, their context and the caveats: %s", out)
		}
		full := detail == query.DetailFull
		if strings.Contains(out, `"selected"`) != full || strings.Contains(out, `"published_row"`) != full {
			t.Fatalf("detail %q has the wrong shape: %s", detail, out)
		}
	}
	if bad := call(t, s, "get_model_details", map[string]any{"model": "claude-opus-5-medium", "detail": "summary"}); !bad.IsError {
		t.Fatal("get_model_details has two projections; an unknown one must be a tool error")
	}
	status := call(t, s, "data_status", map[string]any{})
	if status.IsError || !strings.Contains(text(status), `"usable":true`) {
		t.Fatalf("data_status: %s", text(status))
	}
	for _, detail := range []string{"", "full"} {
		args := map[string]any{}
		if detail != "" {
			args["detail"] = detail
		}
		describe := call(t, s, "describe_available_data", args)
		if describe.IsError || !strings.Contains(text(describe), "bfcl_overall_accuracy") {
			t.Fatalf("describe_available_data %q: %s", detail, text(describe))
		}
		directDescribe, err := svc.Describe(context.Background(), detail)
		if err != nil {
			t.Fatal(err)
		}
		var a, b any
		raw, _ := json.Marshal(describe.StructuredContent)
		json.Unmarshal(raw, &a)
		raw, _ = json.Marshal(directDescribe)
		json.Unmarshal(raw, &b)
		if !reflect.DeepEqual(a, b) {
			t.Fatalf("MCP and service describe differ for detail %q", detail)
		}
		if hasBasis := strings.Contains(text(describe), `"access_basis"`); hasBasis != (detail == "full") {
			t.Fatalf("access_basis present=%v for detail %q", hasBasis, detail)
		}
	}
	if bad := call(t, s, "describe_available_data", map[string]any{"detail": "verbose"}); !bad.IsError {
		t.Fatal("invalid describe detail must be a tool error")
	}
}

func TestToolsFailClearlyWithoutACatalog(t *testing.T) {
	s := connect(t, service(t, false))
	res := call(t, s, "query_models", map[string]any{})
	if !res.IsError || !strings.Contains(text(res), "no Devin dataset is selected") {
		t.Fatalf("query without a catalog: error=%v %s", res.IsError, text(res))
	}
	res = call(t, s, "get_model_details", map[string]any{"model": "claude-opus-5-medium"})
	if !res.IsError {
		t.Fatal("details without a catalog must be an error")
	}
	res = call(t, s, "query_models", map[string]any{"criteria": []any{map[string]any{"metric": "x", "op": "gte", "value": 1}}})
	if !res.IsError {
		t.Fatal("an invalid query must be an error")
	}
}
