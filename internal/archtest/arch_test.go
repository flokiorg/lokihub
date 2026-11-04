// Package archtest holds the import-boundary rules that keep lokihub usable as
// a library: foundation packages must not reach up into the domain, and a few
// packages must stay leaves. A failing rule names the offending import edge.
package archtest

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

const mod = "github.com/flokiorg/lokihub"

type pkg struct {
	ImportPath string
	Imports    []string
}

// load returns the non-test imports of every package in the module that is
// built on the default tags.
func load(t *testing.T) map[string][]string {
	t.Helper()
	out, err := exec.Command("go", "list", "-e", "-json", mod+"/...").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	deps := map[string][]string{}
	dec := json.NewDecoder(strings.NewReader(string(out)))
	for dec.More() {
		var p pkg
		if err := dec.Decode(&p); err != nil {
			t.Fatalf("decode go list output: %v", err)
		}
		short := strings.TrimPrefix(p.ImportPath, mod+"/")
		if p.ImportPath == mod {
			short = "."
		}
		for _, imp := range p.Imports {
			if imp == mod || strings.HasPrefix(imp, mod+"/") {
				deps[short] = append(deps[short], strings.TrimPrefix(imp, mod+"/"))
			}
		}
		if _, ok := deps[short]; !ok {
			deps[short] = nil
		}
	}
	return deps
}

func under(path string, roots ...string) bool {
	for _, r := range roots {
		if path == r || strings.HasPrefix(path, r+"/") {
			return true
		}
	}
	return false
}

// Foundation packages (infrastructure and primitives) must not depend on the
// cash/circle domain, the protocol layers or the transports. Importing config
// or db must never drag the domain in.
//
// The rule is on the exact packages listed, not their sub-packages. Known debt,
// deliberately outside it: db/archive imports cash/cashrequest (archiving a
// bill retires its request records) and lnclient/flnd imports transactions and
// nip47 (the flnd implementation reports payments and notifications). Neither
// is imported by the packages below.
func TestFoundationDoesNotImportDomain(t *testing.T) {
	foundation := []string{
		"config", "db", "db/queries", "db/migrations", "logger", "events",
		"clock", "seal", "buildinfo", "constants", "lnclient", "decodepay",
		"seed", "numeric", "netutil", "nostr/pool", "atrest",
	}
	above := []string{
		"cash", "circle", "transactions", "apps", "ledger", "listing", "keys",
		"nip47", "service", "api", "http", "wails", "lsps", "lspd", "hubinfo",
		"appstore", "nodesign", "connstr", "internal/testknobs",
	}
	deps := load(t)
	for _, f := range foundation {
		for _, imp := range deps[f] {
			if under(imp, above...) {
				t.Errorf("%s imports %s: foundation package reaches into a higher layer", f, imp)
			}
		}
	}
}

// These packages are leaves on purpose and must stay importable on their own.
func TestLeavesImportNothingFromLokihub(t *testing.T) {
	leaves := []string{
		"clock", "seal", "buildinfo", "constants", "seed", "decodepay",
		"lnclient", "numeric", "netutil", "nostr/pool",
		"logger/crashlog", "ttlcache", "nostr/pubkeyhex",
	}
	deps := load(t)
	for _, l := range leaves {
		if imps := deps[l]; len(imps) > 0 {
			t.Errorf("%s must be a leaf but imports %v", l, imps)
		}
	}
}

// lspd is a separately deployed daemon: it imports no Hub package, so it can be
// rebuilt, re-versioned and run remotely on its own. Where it needs something
// the Hub also has (a pubkey check, an npub decode) it keeps its own copy on
// purpose; that is the price of the boundary.
func TestLspdImportsNoHubPackage(t *testing.T) {
	deps := load(t)
	for pk, imps := range deps {
		if !under(pk, "lspd", "cmd/lspd", "cmd/lspd_revenue_cli") {
			continue
		}
		for _, imp := range imps {
			if !under(imp, "lspd", "cmd/lspd", "cmd/lspd_revenue_cli") {
				t.Errorf("%s imports %s: lspd must not import a Hub package", pk, imp)
			}
		}
	}
}

// The Hub's client to the lspd daemon is the only coupling between them, over a
// network boundary: it replicates the JSON shapes and never imports lspd.
func TestDaemonClientDoesNotImportLspd(t *testing.T) {
	deps := load(t)
	for pk, imps := range deps {
		if !under(pk, "lsps/daemonclient") {
			continue
		}
		for _, imp := range imps {
			if under(imp, "lspd") {
				t.Errorf("%s imports %s: the daemon client must not import lspd", pk, imp)
			}
		}
	}
}
