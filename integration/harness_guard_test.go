//go:build integration

package integration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestHarness_NoRemovedConnectionForms (U-20.R2a): the suite reaches Hubs, bills
// and circle wallets only through the signed strings, tokens and join details
// NIP-CASH and NIP-CW define. No URI form outside the simple-wallet path of
// nwcclient, no removed package and no removed field survives in it.
func TestHarness_NoRemovedConnectionForms(t *testing.T) {
	banned := []string{"lokihub/" + "lokicash", "mint_signature", "pairing_uri", "PairingURI", "jit_", "claim_funds", "CircleWalletURI"}
	err := filepath.Walk(".", func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") || path == "harness_guard_test.go" {
			return err
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		text := string(body)
		for _, word := range banned {
			// nwcclient keeps the URI form for the simple-wallet fixtures only.
			if strings.HasPrefix(path, "nwcclient/") && word == "PairingURI" {
				continue
			}
			if strings.Contains(text, word) {
				t.Errorf("%s still names %q", path, word)
			}
		}
		if !strings.HasPrefix(path, "nwcclient/") && strings.Contains(text, "nostr+walletconnect") {
			t.Errorf("%s builds a URI form", path)
		}
		return nil
	})
	require.NoError(t, err)
}
