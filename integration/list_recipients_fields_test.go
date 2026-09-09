//go:build integration

package integration

import (
	"testing"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
)

// TestListRecipients_SurfacesMinTransferMlokiAndExpiresAt is the live-node
// counterpart to the unit-level fix
// (TestHandleListRecipientsEvent_SurfacesMinTransferMlokiAndExpiresAt):
// list_recipients now carries both a slice's min_transfer_millis floor and the
// shared wallet's own expires_at deadline, over the real wire, not just in
// the Go struct.
func TestListRecipients_SurfacesMinTransferMlokiAndExpiresAt(t *testing.T) {
	cfg := requireConfig(t)
	admin, ok := newAdminClient(cfg)
	if !ok {
		t.Skip("skipping: admin_api not configured - see integration/README.md")
	}

	const minTransferMloki = happyPathAmountMloki

	req := adminCreateAppRequest{
		Name:                  ephemeralFixtureNamePrefix + " list-recipients-fields-hub",
		Scopes:                []string{constants.CASH_HUB_SCOPE, constants.PAY_INVOICE_SCOPE, constants.MAKE_INVOICE_SCOPE, constants.GET_BALANCE_SCOPE},
		Kind:                  "cash_hub",
		CashPerWalletMaxMloki: 10_000_000,
		CashMaxExpSecs:        3600,
		CashMinTransferMloki:  minTransferMloki,
	}
	resp, err := admin.createApp(req)
	require.NoError(t, err)
	t.Cleanup(func() {
		if err := admin.deleteApp(resp.ID); err != nil {
			t.Logf("cleanup: failed to delete ephemeral hub app_id=%d (%v)", resp.ID, err)
		}
	})
	t.Cleanup(func() {
		claims, err := admin.listCashWalletClaims(resp.ID)
		if err != nil {
			return
		}
		seen := map[uint]bool{}
		for _, claim := range claims {
			if seen[claim.WalletAppID] {
				continue
			}
			seen[claim.WalletAppID] = true
			_ = admin.deleteCashWallet(resp.ID, claim.WalletAppID)
		}
	})
	require.NoError(t, admin.transfer(nil, resp.ID, ephemeralCashHubFundLoki))
	hubClient := mustConnect(t, resp.PairingUri)

	beneficiaryPub, err := nostr.GetPublicKey(newTestPrivkey(t))
	require.NoError(t, err)

	var created MintCashResult
	require.NoError(t, hubClient.Call(ctxT(t), constants.NIP47MethodMintCash, MintCashParams{
		Recipients: onePubkeyRecipient(beneficiaryPub, minTransferMloki*2),
		Expiry:     happyPathExpirySecs,
	}, &created))

	shared := mustConnect(t, created.PairingURI)

	var recipients ListRecipientsResult
	require.NoError(t, shared.Call(ctxT(t), constants.NIP47MethodListRecipients, struct{}{}, &recipients))
	require.Len(t, recipients.Recipients, 1)
	recipient := recipients.Recipients[0]

	require.EqualValues(t, minTransferMloki, recipient.MinTransferMillis,
		"a recipient must be able to learn the hub's inherited split floor without a failed cash_transfer attempt first")
	require.NotNil(t, recipient.ExpiresAt, "a recipient must be able to learn the wallet's redemption deadline over the wire")
	require.Greater(t, *recipient.ExpiresAt, int64(0))
}
