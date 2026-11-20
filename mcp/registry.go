package mcp

import "sort"

// ToolDescriptor is a single MCP tool derived from the classification policy.
// It is the single source both the advertised catalog and the scope gate read,
// so they cannot drift. Handlers, input/output schemas and snake_case tool
// names are attached in later milestones; M0 fixes identity, group and mode.
type ToolDescriptor struct {
	// APIMethod is the api.API method this tool adapts (its stable identity).
	APIMethod string
	Group     Group
	Mode      Mode
	// Guarded marks the irreversible class needing the residual guard.
	Guarded bool
}

// Classification returns the policy decision backing this tool.
func (t ToolDescriptor) Classification() Classification {
	return Classification{Kind: ClassTool, Group: t.Group, Mode: t.Mode, Guarded: t.Guarded}
}

// Registry is the set of tools the MCP server exposes. It is always derived
// from the classification policy (never hand-listed), so a tool can only exist
// for a method the policy marks ClassTool.
type Registry struct {
	tools []ToolDescriptor
	byAPI map[string]ToolDescriptor
}

// NewRegistry builds the registry from the classification policy: one tool per
// ClassTool method.
func NewRegistry() *Registry {
	r := &Registry{byAPI: map[string]ToolDescriptor{}}
	for name, c := range methodClass {
		if c.Kind != ClassTool {
			continue
		}
		td := ToolDescriptor{APIMethod: name, Group: c.Group, Mode: c.Mode, Guarded: c.Guarded}
		r.tools = append(r.tools, td)
		r.byAPI[name] = td
	}
	sort.Slice(r.tools, func(i, j int) bool { return r.tools[i].APIMethod < r.tools[j].APIMethod })
	return r
}

// Tools returns every registered tool, sorted by API method name.
func (r *Registry) Tools() []ToolDescriptor { return r.tools }

// ByAPIMethod looks up a tool by its api.API method name.
func (r *Registry) ByAPIMethod(method string) (ToolDescriptor, bool) {
	td, ok := r.byAPI[method]
	return td, ok
}

// Authorize is the gate entry point for a registered tool: a connection holding
// scopes may run the tool named by api method only if the policy classifies it
// as a tool and the connection holds its group+mode scope.
func (r *Registry) Authorize(scopes []string, method string) error {
	c, ok := classify(method)
	if !ok {
		// Unknown method: fail closed. The ratchet test guarantees this never
		// happens for a real api.API method, but a typo must not fall open.
		return ErrNotATool
	}
	return Gate(scopes, c)
}
