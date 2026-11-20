package mcp

import (
	"reflect"
	"sort"
	"testing"

	"github.com/flokiorg/lokihub/api"
	"github.com/flokiorg/lokihub/nip47/permissions"
)

// apiMethodNames reflects every method of the api.API interface (direct plus
// the embedded ListingsAPI / LSPCatalogueAPI, which reflection promotes).
func apiMethodNames() []string {
	t := reflect.TypeOf((*api.API)(nil)).Elem()
	names := make([]string, 0, t.NumMethod())
	for i := 0; i < t.NumMethod(); i++ {
		names = append(names, t.Method(i).Name)
	}
	sort.Strings(names)
	return names
}

// TestExclusionRatchet is the load-bearing completeness gate: every api.API
// method MUST be classified (tool / excluded / not-applicable). A new core
// method with no classification fails this test, forcing a conscious decision
// in policy.go rather than silently appearing (or not) on the MCP surface.
func TestExclusionRatchet(t *testing.T) {
	var unclassified []string
	for _, name := range apiMethodNames() {
		if _, ok := methodClass[name]; !ok {
			unclassified = append(unclassified, name)
		}
	}
	if len(unclassified) > 0 {
		t.Fatalf("api.API methods with no MCP classification (add them to methodClass in policy.go): %v", unclassified)
	}

	// And the reverse: methodClass must not name a method that no longer
	// exists on api.API (stale entry), which would mask a real gap.
	live := map[string]bool{}
	for _, n := range apiMethodNames() {
		live[n] = true
	}
	var stale []string
	for name := range methodClass {
		if !live[name] {
			stale = append(stale, name)
		}
	}
	if len(stale) > 0 {
		sort.Strings(stale)
		t.Fatalf("methodClass names methods not on api.API (remove the stale entries): %v", stale)
	}
}

// TestEveryToolHasValidGroupAndMode is the fail-open regression: a tool with an
// empty/invalid group or mode would bypass the gate (Gate derives the required
// scope from them). This mirrors the real fail-open that already bit the hub's
// NWC gate (nip47/auditB_secb_scope_failopen_test.go).
func TestEveryToolHasValidGroupAndMode(t *testing.T) {
	for name, c := range methodClass {
		if c.Kind != ClassTool {
			continue
		}
		if !c.Group.Valid() {
			t.Errorf("tool %q has invalid group %q", name, c.Group)
		}
		if !c.Mode.Valid() {
			t.Errorf("tool %q has invalid mode %q", name, c.Mode)
		}
	}
}

// TestExcludedAndNAHaveReason keeps the policy self-documenting and catches a
// zero-value Classification{} accidentally landing as ClassTool.
func TestExcludedAndNAHaveReason(t *testing.T) {
	for name, c := range methodClass {
		switch c.Kind {
		case ClassExcluded, ClassNotApplicable:
			if c.Reason == "" {
				t.Errorf("method %q is excluded/NA with no reason", name)
			}
		case ClassTool:
			// nothing
		default:
			t.Errorf("method %q has unknown ClassKind %d", name, c.Kind)
		}
	}
}

// TestGateDeniesWithoutScope: no grant → every tool denied.
func TestGateDeniesWithoutScope(t *testing.T) {
	for name, c := range methodClass {
		if c.Kind != ClassTool {
			continue
		}
		if err := Gate(nil, c); err == nil {
			t.Errorf("tool %q allowed with no scopes", name)
		}
	}
}

// TestGateExcludedNeverAllowed: excluded/NA methods are never runnable, with
// any scope set (even a hypothetical superset of every mcp scope).
func TestGateExcludedNeverAllowed(t *testing.T) {
	everything := AllScopes()
	for name, c := range methodClass {
		if c.Kind == ClassTool {
			continue
		}
		if err := Gate(everything, c); err != ErrNotATool {
			t.Errorf("non-tool %q (%d) was not refused with ErrNotATool: %v", name, c.Kind, err)
		}
	}
}

// TestReadGrantBlocksWriteButAllowsRead: a Read-only grant for a group must
// allow that group's read tools and deny its write tools; a Write grant must
// allow its write tools.
func TestReadGrantBlocksWriteButAllowsRead(t *testing.T) {
	for _, g := range AllGroups {
		readOnly := []string{g.Scope(ModeRead)}
		readWrite := []string{g.Scope(ModeRead), g.Scope(ModeWrite)}
		for name, c := range methodClass {
			if c.Kind != ClassTool || c.Group != g {
				continue
			}
			switch c.Mode {
			case ModeRead:
				if err := Gate(readOnly, c); err != nil {
					t.Errorf("read tool %q denied under read grant for %s: %v", name, g, err)
				}
			case ModeWrite:
				if err := Gate(readOnly, c); err == nil {
					t.Errorf("write tool %q allowed under read-only grant for %s", name, g)
				}
				if err := Gate(readWrite, c); err != nil {
					t.Errorf("write tool %q denied under read+write grant for %s: %v", name, g, err)
				}
			}
		}
	}
}

// TestCrossGroupDenied: a grant for one group never authorizes another group's
// tools (horizontal scope confusion).
func TestCrossGroupDenied(t *testing.T) {
	for _, granted := range AllGroups {
		scopes := []string{granted.Scope(ModeRead), granted.Scope(ModeWrite)}
		for name, c := range methodClass {
			if c.Kind != ClassTool || c.Group == granted {
				continue
			}
			if err := Gate(scopes, c); err == nil {
				t.Errorf("tool %q (group %s) allowed by a %s grant", name, c.Group, granted)
			}
		}
	}
}

// TestScopesDisjointFromNWC: mcp:* scopes must not collide with the NIP-47
// wallet scope vocabulary — the gate here is a separate authority.
func TestScopesDisjointFromNWC(t *testing.T) {
	nwc := map[string]bool{}
	for _, s := range permissions.AllScopes() {
		nwc[s] = true
	}
	for _, s := range AllScopes() {
		if nwc[s] {
			t.Errorf("mcp scope %q collides with an NWC scope", s)
		}
		if len(s) < len(ScopePrefix) || s[:len(ScopePrefix)] != ScopePrefix {
			t.Errorf("mcp scope %q is not namespaced with %q", s, ScopePrefix)
		}
	}
}

// TestGuardedAreIrreversibleWrites: the residual-guard set is exactly the
// irreversible class, all classified as write tools.
func TestGuardedAreIrreversibleWrites(t *testing.T) {
	want := map[string]bool{
		"RedeemOnchainFunds": true,
		"OpenChannel":        true,
		"CloseChannel":       true,
	}
	got := map[string]bool{}
	for _, name := range GuardedMethods() {
		got[name] = true
		c := methodClass[name]
		if c.Kind != ClassTool || c.Mode != ModeWrite {
			t.Errorf("guarded method %q must be a write tool, got %+v", name, c)
		}
	}
	if !reflect.DeepEqual(want, got) {
		t.Errorf("guarded set = %v, want %v", keys(got), keys(want))
	}
}

// TestRegistryOnlyToolMethods: the registry contains exactly the ClassTool
// methods and nothing excluded/NA.
func TestRegistryOnlyToolMethods(t *testing.T) {
	r := NewRegistry()
	if len(r.Tools()) == 0 {
		t.Fatal("registry is empty")
	}
	for _, td := range r.Tools() {
		c, ok := classify(td.APIMethod)
		if !ok || c.Kind != ClassTool {
			t.Errorf("registry exposes non-tool method %q", td.APIMethod)
		}
	}
	// Excluded methods must never be registered.
	for name, c := range methodClass {
		if c.Kind == ClassTool {
			continue
		}
		if _, ok := r.ByAPIMethod(name); ok {
			t.Errorf("registry exposes excluded/NA method %q", name)
		}
	}
}

// TestRatchetCatchesForgottenMethod (M-EXCL-1, whitebox): prove the ratchet has
// teeth — if a real api.API method were left unclassified, the completeness
// check that TestExclusionRatchet runs must flag it. We simulate a "forgotten"
// method by removing one live entry from a copy of the classification and
// re-running the same partition logic.
func TestRatchetCatchesForgottenMethod(t *testing.T) {
	names := apiMethodNames()
	if len(names) == 0 {
		t.Fatal("no api.API methods reflected")
	}
	victim := names[0]

	// Copy methodClass minus the victim — the "forgotten classification" case.
	cp := make(map[string]Classification, len(methodClass))
	for k, v := range methodClass {
		if k == victim {
			continue
		}
		cp[k] = v
	}

	var unclassified []string
	for _, name := range names {
		if _, ok := cp[name]; !ok {
			unclassified = append(unclassified, name)
		}
	}
	if len(unclassified) != 1 || unclassified[0] != victim {
		t.Fatalf("ratchet logic failed to flag a forgotten method: got %v, want [%s]", unclassified, victim)
	}
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
