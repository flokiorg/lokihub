package cashwallet

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// goFilesOfModule lists the module's Go files, outside vendored and generated
// trees.
func goFilesOfModule(t *testing.T) []string {
	t.Helper()
	root, err := filepath.Abs("..")
	require.NoError(t, err)
	var out []string
	require.NoError(t, filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "node_modules", "dist", ".git", ".claude", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".go") {
			out = append(out, path)
		}
		return nil
	}))
	require.NotEmpty(t, out)
	return out
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path) //nolint:gosec // test reads its own module
	require.NoError(t, err)
	return string(raw)
}
