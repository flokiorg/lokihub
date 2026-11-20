package api

import "testing"

func TestSafeExplorerEndpoint(t *testing.T) {
	ok := []string{
		"/tx/" + "ab12",
		"/v1/lightning/nodes/02abcdef",
		"/v1/transaction-times?txId[]=ab12",
		"/v1/fees/recommended",
		"/address/bcrt1qxyz/utxo",
	}
	bad := []string{
		"", "tx/ab", "/../admin", "/tx/../../x", "//evil.example/x", "/x@evil.example",
		"/x#frag", "/x\\y", "/x y", "/x\ny", "/x\x00",
	}
	for _, e := range ok {
		if !safeExplorerEndpoint(e) {
			t.Errorf("legitimate endpoint refused: %q", e)
		}
	}
	for _, e := range bad {
		if safeExplorerEndpoint(e) {
			t.Errorf("unsafe endpoint accepted: %q", e)
		}
	}
}
