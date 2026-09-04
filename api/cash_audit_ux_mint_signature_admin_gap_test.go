package api

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/cashwallet"
)

// TestCreateCashWalletRequest_MintProvenanceReachableFromAdminAPI is the
// regression guard for a UX/experience-review finding (Cash Hub consolidate/
// split audit, 2026-08-29; fixed during a later full-review pass): mint
// provenance (NIP-CASH §Mint Provenance, opt-in via mint_signature) was fully
// implemented in the shared engine (cashwallet.Params.SignMint) and exposed
// on the NWC-facing mint_cash/cash_transfer/cash_consolidate request shapes,
// but was completely absent from CreateCashWalletRequest, the admin HTTP wire
// type api.CreateCashWallet decodes -- so a Hub owner/operator using the
// admin UI (the one surface they're documented to configure Cash Hubs from,
// frontend/src/screens/subwallets/CashHubAllocations.tsx) had no way to ever
// opt a wallet into mint provenance; the feature was reachable only by an NWC
// client calling mint_cash directly. CreateCashWalletRequest.MintSignature
// now threads through to cashwallet.Params.SignMint in api.CreateCashWallet.
func TestCreateCashWalletRequest_MintProvenanceReachableFromAdminAPI(t *testing.T) {
	// The engine supports it...
	engineType := reflect.TypeOf(cashwallet.Params{})
	_, engineHasField := engineType.FieldByName("SignMint")
	require.True(t, engineHasField, "cashwallet.Params dropped SignMint -- re-verify this finding, mint provenance may have been removed entirely")

	// ...and the admin HTTP request type that CashHubAllocations.tsx drives
	// now has a way to ask for it.
	adminType := reflect.TypeOf(CreateCashWalletRequest{})
	mintSignatureField, ok := adminType.FieldByName("MintSignature")
	require.True(t, ok, "CreateCashWalletRequest must expose a MintSignature field")
	assert.Equal(t, "mint_signature,omitempty", mintSignatureField.Tag.Get("json"))
}
