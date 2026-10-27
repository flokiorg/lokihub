package cashwallet

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// U-2.10: no legacy provenance label survives anywhere in the sources. Every bill
// is signed under nmilat's cash-mint:v1 or cash-zap:v1 (NIP-CASH §Mint Provenance),
// and no compatibility path reads the old ones. U-2.R2a: nothing imports the
// deleted lokicash package.
func TestU_2_10_NoLegacyProvenanceLabels(t *testing.T) {
	root, err := filepath.Abs("..")
	require.NoError(t, err)
	banned := []string{"lokicash" + "-mint", "satscash" + "-mint", "satscash" + "-zap", "lokihub/" + "lokicash\""}
	var hits []string
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "node_modules", ".git", "dist", "docs", "tmp", "data", ".claude":
				return filepath.SkipDir
			}
			return nil
		}
		ext := filepath.Ext(path)
		if ext != ".go" && ext != ".ts" && ext != ".tsx" && !(ext == ".json" && strings.Contains(path, "i18n")) {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, b := range banned {
			if strings.Contains(string(body), b) {
				hits = append(hits, path+": "+b)
			}
		}
		return nil
	})
	require.NoError(t, err)
	require.Empty(t, hits)
}
