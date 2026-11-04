package cashwallet

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// U-5.12: a bill's credential is its cash_token (NIP-CASH §Minting Cash,
// Response). Nothing in cashwallet, the cash and circle controllers or the cash
// and circle API handlers builds or parses the URI form; ordinary NWC app
// connections keep it.
func TestNoURIFormInCashOrCircleCode(t *testing.T) {
	root, err := filepath.Abs("..")
	require.NoError(t, err)
	banned := map[string]bool{"Build" + "PairingURI": true, "Parse" + "PairingURI": true}

	// whole files
	var files []string
	for _, pattern := range []string{"cashwallet/*.go", "nip47/controllers/*cash*.go", "nip47/controllers/*circle*.go"} {
		m, err := filepath.Glob(filepath.Join(root, pattern))
		require.NoError(t, err)
		files = append(files, m...)
	}
	// only the cash and circle functions of the API package
	apiFiles, err := filepath.Glob(filepath.Join(root, "api/*.go"))
	require.NoError(t, err)

	check := func(path string, onlyCashOrCircle bool) {
		if strings.HasSuffix(path, "_test.go") {
			return
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, nil, 0)
		require.NoError(t, err)
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			if onlyCashOrCircle && !strings.Contains(fn.Name.Name, "Cash") && !strings.Contains(fn.Name.Name, "Circle") {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if ok && banned[sel.Sel.Name] {
					assert.Fail(t, "URI form in cash or circle code", "%s: %s calls %s", fset.Position(sel.Pos()), fn.Name.Name, sel.Sel.Name)
				}
				return true
			})
		}
	}
	for _, f := range files {
		check(f, false)
	}
	for _, f := range apiFiles {
		check(f, true)
	}
	require.NotEmpty(t, files)
}
