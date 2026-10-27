package nip47

import (
	"strings"
	"testing"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/db"
)

// U-13.10 (part): several app rows sharing a circle's wallet pubkey resolve to the
// circle_hub row, and its bill proof verifies against a live connection of the
// Hub (VerifyBillProofAny's rule).
func TestResolveItemTarget_PrefersTheCircleHubRowOverAnotherSharingItsKey(t *testing.T) {
	h := newJoinHub(t)
	// A row created before the Hub's (lower id), sharing its wallet pubkey.
	shared := strings.Repeat("9a", 32)
	other := db.App{Name: "listing", AppPubkey: strings.Repeat("cd", 32), WalletPubkey: &shared, Kind: db.AppKindStandard}
	require.NoError(t, h.ts.DB.Create(&other).Error)
	hub := db.App{Name: "hub", AppPubkey: strings.Repeat("ce", 32), WalletPubkey: &shared, Kind: db.AppKindCircleHub}
	require.NoError(t, h.ts.DB.Create(&hub).Error)
	require.Less(t, other.ID, hub.ID)

	got, err := h.svc.resolveItemTarget(shared)
	require.NoError(t, err)
	assert.Equal(t, hub.ID, got.ID)

	assert.True(t, billProofLive(got.AppPubkey, liveHubConnKeys(&got)))
	assert.False(t, billProofLive(other.AppPubkey, liveHubConnKeys(&got)), "a key that is no live connection of the Hub")
	assert.False(t, billProofLive("", liveHubConnKeys(&got)))
}

// A join lookup still resolves once the Hub is gone, through the connections its
// retained join records name (WP-14: "live or via hub_wallet_pubkey").
func TestLookupTargetKeys_DeletedHubThroughJoinRecords(t *testing.T) {
	h := newJoinHub(t)
	gone := strings.Repeat("ef", 32)
	conn := strings.Repeat("12", 32)
	require.NoError(t, h.ts.DB.Create(&db.CircleJoinRecord{
		CircleHubAppID: 999, HubWalletPubkey: gone, HubConnPubkey: conn,
		AuthorizerPubkey: nostr.GeneratePrivateKey(), ConnectionPubkey: strings.Repeat("34", 32),
		ProofCreatedAt: 1, Deadline: 301, EncryptedDetails: "x", WalletAppID: 1,
	}).Error)

	keys, err := h.svc.lookupTargetKeys(gone)
	require.NoError(t, err)
	assert.Equal(t, []string{conn}, keys)

	keys, err = h.svc.lookupTargetKeys(*h.app.WalletPubkey)
	require.NoError(t, err)
	assert.Equal(t, []string{h.app.AppPubkey}, keys)

	keys, err = h.svc.lookupTargetKeys(strings.Repeat("56", 32))
	require.NoError(t, err)
	assert.Empty(t, keys)
}
