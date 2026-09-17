// Package devmodelcatalog embeds repository-level assets into the binary.
//
// AGENTS.md is the single source of agent guidance. The CLI (`devmodels
// agents-md`) and the MCP tool (`agents_md`) both return this exact content, so
// there is no second copy to drift.
package devmodelcatalog

import _ "embed"

// AgentsMD is the exact content of AGENTS.md.
//
//go:embed AGENTS.md
var AgentsMD string
