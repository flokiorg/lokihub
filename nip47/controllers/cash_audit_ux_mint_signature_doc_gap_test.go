package controllers

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// nipCashDocSection extracts the substring of NIP-CASH.md's content starting
// at the first line matching headingPrefix (a literal "## ..." or "### ..."
// heading) up to (not including) the next line that starts with "#" at the
// SAME OR SHALLOWER level -- i.e. everything under that heading, including
// its own subsections, stopping only at a sibling or higher heading. Fails
// the test outright if headingPrefix isn't found, so a doc rename surfaces as
// a loud test failure here rather than this test silently checking nothing.
func nipCashDocSection(t *testing.T, headingPrefix string) string {
	t.Helper()
	// nip47/controllers -> repo root is two levels up.
	raw, err := os.ReadFile("../../docs/nips/NIP-CASH.md")
	require.NoError(t, err, "NIP-CASH.md must be readable at its expected repo path")
	content := string(raw)

	level := strings.IndexFunc(headingPrefix, func(r rune) bool { return r != '#' })
	require.Greater(t, level, 0, "headingPrefix must start with one or more '#'")

	lines := strings.Split(content, "\n")
	start := -1
	for i, line := range lines {
		if strings.HasPrefix(line, headingPrefix) {
			start = i
			break
		}
	}
	require.GreaterOrEqual(t, start, 0, "heading %q not found in NIP-CASH.md -- doc structure changed, re-verify this test's target section", headingPrefix)

	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		trimmed := lines[i]
		hashes := strings.IndexFunc(trimmed, func(r rune) bool { return r != '#' })
		if hashes > 0 && hashes <= level && strings.HasPrefix(trimmed, "#") {
			end = i
			break
		}
	}
	return strings.Join(lines[start:end], "\n")
}

// TestNIPCashDoc_MintSignatureRequestFieldDocumented is the regression guard
// for a UX/experience-review finding (Cash Hub consolidate/split audit,
// 2026-08-29; confirmed and fixed during a later full-review pass):
// NIP-CASH.md thoroughly documented mint provenance from the TOKEN's point of
// view (§Mint Provenance -- what TLV types 5/6 mean, what they prove, that
// it's "optional by default"), but never documented the REQUEST-side opt-in
// that actually produces one. All three methods that accept it --
// mint_cash (mintCashParams.MintSignature), cash_transfer
// (cashTransferParams.MintSignature), and cash_consolidate
// (cashConsolidateParams.MintSignature) -- carry a real `mint_signature` JSON
// field; a client author building against the spec alone (the ONLY source of
// truth for a recipient/NWC-client author -- there is no frontend on that
// side of any of the three) had no way to discover this parameter exists
// without reading the Go source directly. Each method's own "### Request"
// section now documents it.
func TestNIPCashDoc_MintSignatureRequestFieldDocumented(t *testing.T) {
	mintSection := nipCashDocSection(t, "## Minting a Cash Wallet")
	assert.Contains(t, mintSection, "mint_signature",
		"mint_cash's own spec section must document the mint_signature request field")

	transferSection := nipCashDocSection(t, "## Transferring and Splitting a Slice")
	assert.Contains(t, transferSection, "mint_signature",
		"cash_transfer's own spec section must document the mint_signature request field")

	consolidateSection := nipCashDocSection(t, "## Consolidating Tokens")
	assert.Contains(t, consolidateSection, "mint_signature",
		"cash_consolidate's own spec section must document the mint_signature request field")

	// Ground-truth: the parameter is real and reachable in the implementation,
	// so the doc coverage above is describing a real field, not a stale one.
	mintType := reflect.TypeOf(mintCashParams{})
	mintField, ok := mintType.FieldByName("MintSignature")
	require.True(t, ok, "mintCashParams dropped MintSignature -- re-verify this finding")
	assert.Equal(t, "mint_signature,omitempty", mintField.Tag.Get("json"))

	transferType := reflect.TypeOf(cashTransferParams{})
	transferField, ok := transferType.FieldByName("MintSignature")
	require.True(t, ok, "cashTransferParams dropped MintSignature -- re-verify this finding")
	assert.Equal(t, "mint_signature,omitempty", transferField.Tag.Get("json"))

	consolidateType := reflect.TypeOf(cashConsolidateParams{})
	consolidateField, ok := consolidateType.FieldByName("MintSignature")
	require.True(t, ok, "cashConsolidateParams dropped MintSignature -- re-verify this finding")
	assert.Equal(t, "mint_signature,omitempty", consolidateField.Tag.Get("json"))
}
