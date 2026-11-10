package lnclient

import (
	"strings"
	"testing"
)

func TestDecodeHash32(t *testing.T) {
	good := strings.Repeat("ab", 32)
	if b, err := DecodeHash32("payment hash", good); err != nil || len(b) != 32 {
		t.Fatalf("valid hash rejected: %v", err)
	}
	for _, bad := range []string{"", "zz", strings.Repeat("ab", 31), strings.Repeat("ab", 33)} {
		if _, err := DecodeHash32("payment hash", bad); err == nil {
			t.Errorf("%q accepted", bad)
		} else if !strings.Contains(err.Error(), "payment hash must be 32 bytes hex") {
			t.Errorf("%q: unexpected error %v", bad, err)
		}
	}
}
