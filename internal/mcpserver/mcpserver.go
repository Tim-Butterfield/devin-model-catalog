// Package mcpserver exposes the application service over the Model Context
// Protocol. Every tool calls the same app.Service methods the CLI uses; no
// tool shells out or re-implements catalog logic.
//
// The tool declarations are deliberately terse. A client downloads them on
// every connection, before it knows whether it will use the catalog at all, so
// they say what a request must contain and leave what the values mean to
// agents_md and describe_available_data, which are read once and on demand.
package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	devmodelcatalog "github.com/Tim-Butterfield/devin-model-catalog"
	"github.com/Tim-Butterfield/devin-model-catalog/internal/app"
	"github.com/Tim-Butterfield/devin-model-catalog/internal/buildinfo"
	"github.com/Tim-Butterfield/devin-model-catalog/internal/query"
)

// Instructions are sent to clients on initialization.
const Instructions = "Devin Model Catalog: factual model, price and benchmark evidence for the selected Devin licensing dataset. " +
	"Read agents_md once for how to use it. Check data_status before relying on results, call describe_available_data to see " +
	"which metrics exist, then query_models with criteria you derive from the user's task. The server never chooses criteria for you."

// NoInput is the input of tools that take no arguments.
type NoInput struct{}

// DetailInput selects a response projection.
type DetailInput struct {
	Detail string `json:"detail,omitempty" jsonschema:"summary (default) or full"`
}

// ModelInput identifies a model and the projection to return it in.
type ModelInput struct {
	Model  string `json:"model" jsonschema:"Devin model uid (preferred) or exact label"`
	Detail string `json:"detail,omitempty" jsonschema:"compact (default) or full"`
}

// evidenceDef names the hoisted evidence-policy subschema.
const evidenceDef = "evidence_policy"

// queryInputSchema is the query_models input schema: inferred from
// query.Request, then de-duplicated.
//
// EvidencePolicy is the same type on criteria and on order_by, and schema
// inference inlines it in full both times — one repeated object accounting for
// a large share of the whole tool list. Hoisting it into $defs and pointing
// both places at it sends it once. Because the hoisted subschema is the
// inferred one rather than a hand-written copy, the wire contract still cannot
// drift from the Go type.
//
// It returns nil if the two policies are ever not identical, or inference
// fails; the caller then lets the SDK infer the schema as before. That is
// larger, never wrong.
func queryInputSchema() *jsonschema.Schema {
	schema, err := jsonschema.For[query.Request](nil)
	if err != nil {
		return nil
	}
	criteria, order := itemProperties(schema, "criteria"), itemProperties(schema, "order_by")
	policy := criteria["evidence"]
	if !identicalSchema(policy, order["evidence"]) {
		return nil
	}
	schema.Defs = map[string]*jsonschema.Schema{evidenceDef: policy}
	// Two distinct values, not one shared pointer: the resolver requires the
	// schema to form a tree, and the same node in two places is not one.
	ref := func() *jsonschema.Schema { return &jsonschema.Schema{Ref: "#/$defs/" + evidenceDef} }
	criteria["evidence"], order["evidence"] = ref(), ref()
	return schema
}

// itemProperties returns the properties of the element schema of an array
// property, or nil when the request no longer has that shape.
func itemProperties(schema *jsonschema.Schema, field string) map[string]*jsonschema.Schema {
	prop := schema.Properties[field]
	if prop == nil || prop.Items == nil {
		return nil
	}
	return prop.Items.Properties
}

func identicalSchema(a, b *jsonschema.Schema) bool {
	if a == nil || b == nil {
		return false
	}
	x, xerr := json.Marshal(a)
	y, yerr := json.Marshal(b)
	return xerr == nil && yerr == nil && bytes.Equal(x, y)
}

// New builds a server over svc.
func New(svc *app.Service) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "devmodels", Title: "Devin Model Catalog", Version: buildinfo.String()},
		&mcp.ServerOptions{Instructions: Instructions})

	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true}

	mcp.AddTool(server, &mcp.Tool{
		Name:        "agents_md",
		Title:       "Usage guidance",
		Description: "Return AGENTS.md: how to turn a task into criteria, read results and recommend. Read it once before first use.",
		Annotations: readOnly,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ NoInput) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: devmodelcatalog.AgentsMD}}}, nil, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "data_status",
		Title:       "Data status",
		Description: "Whether the catalog is usable: selected dataset, freshness, each source's last refresh outcome, and problems to relay to the user.",
		Annotations: readOnly,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ NoInput) (*mcp.CallToolResult, any, error) {
		st, err := svc.Status(ctx)
		if err != nil {
			return nil, nil, err
		}
		return nil, st, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "describe_available_data",
		Title:       "Describe available data",
		Description: "The selected dataset's metrics (meaning, unit, better direction, scope, coverage, source and context codes) and the query vocabulary. The default summary is enough to build queries.",
		Annotations: readOnly,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in DetailInput) (*mcp.CallToolResult, any, error) {
		d, err := svc.Describe(ctx, in.Detail)
		if err != nil {
			return nil, nil, err
		}
		return nil, d, nil
	})

	queryTool := &mcp.Tool{
		Name:  "query_models",
		Title: "Query models",
		Description: "Models in the selected dataset meeting every criterion, ordered by order_by. " +
			"Criteria decide eligibility and each needs a missing policy; order_by only orders. " +
			"Counts, exclusions and ambiguities cover every eligible model, so limit 10 is usually enough. " +
			"Equally ranked evidence that disagrees is ambiguous and no value is chosen. See agents_md for reading results.",
		Annotations: readOnly,
	}
	if schema := queryInputSchema(); schema != nil {
		queryTool.InputSchema = schema
	}
	mcp.AddTool(server, queryTool, func(ctx context.Context, _ *mcp.CallToolRequest, req query.Request) (*mcp.CallToolResult, any, error) {
		resp, err := svc.Query(ctx, req)
		if err != nil {
			return nil, nil, err
		}
		return nil, resp, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:  "get_model_details",
		Title: "Model details",
		Description: "Everything known about one model: each metric's value, state, source, evaluation context, weaker-evidence flags and " +
			"peer values, plus missing metrics and caveats. detail full adds observation objects, alternatives and the published Devin row.",
		Annotations: readOnly,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in ModelInput) (*mcp.CallToolResult, any, error) {
		d, err := svc.ModelDetails(ctx, in.Model, in.Detail)
		if err != nil {
			return nil, nil, err
		}
		return nil, d, nil
	})

	return server
}

// Serve runs the server on stdio until the client disconnects.
func Serve(ctx context.Context, svc *app.Service) error {
	return New(svc).Run(ctx, &mcp.StdioTransport{})
}
