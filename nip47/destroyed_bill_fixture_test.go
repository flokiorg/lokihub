package nip47

import (
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

// seedDestroyedBill archives a bill this hub destroyed at endedAt, with its Hub's
// retention, and returns its app id and wallet pubkey.
func seedDestroyedBill(t *testing.T, svc *tests.TestService, endedAt time.Time, retentionSecs int) (walletAppID uint, walletPubkey string) {
	t.Helper()

	hub := db.App{Name: "tombstone-hub", Kind: db.AppKindCashHub}
	require.NoError(t, svc.DB.Create(&hub).Error)
	require.NoError(t, svc.DB.Create(&db.CashHubConfig{
		AppID:              hub.ID,
		PerWalletMaxMloki:  100_000,
		SpentRetentionSecs: retentionSecs,
	}).Error)

	// The bill's app row is gone — that is what makes this the tombstone path
	// — so its id is all that is left to derive keys from.
	walletAppID = hub.ID + 5_000
	walletKey, err := svc.Keys.GetAppWalletKey(walletAppID)
	require.NoError(t, err)
	walletPubkey, err = nostr.GetPublicKey(walletKey)
	require.NoError(t, err)
	// The bill's App.AppPubkey, which archiving copies onto the row.
	pairingKey, err := svc.Keys.GetCashPairingKey(walletAppID)
	require.NoError(t, err)
	connectionPubkey, err := nostr.GetPublicKey(pairingKey)
	require.NoError(t, err)

	// RetainedUntil is materialised on the row, exactly as archive.CashBill does
	// it in production — a fixture that leaves it nil is a bill with no
	// tombstone, since the deadline is no longer derived on read. Retention 0
	// stays nil, which is what "no tombstone" means.
	var retainedUntil *time.Time
	if retentionSecs > 0 {
		deadline := endedAt.Add(time.Duration(retentionSecs) * time.Second)
		retainedUntil = &deadline
	}

	require.NoError(t, svc.DB.Create(&db.CashBillArchive{
		WalletAppID:      walletAppID,
		HubAppID:         hub.ID,
		WalletPubkey:     walletPubkey,
		ConnectionPubkey: connectionPubkey,
		EndedAt:          endedAt,
		RetainedUntil:    retainedUntil,
		Outcome:          db.CashBillOutcomeDrained,
		TotalMloki:       1000,
		FundedMloki:      1000,
	}).Error)
	return walletAppID, walletPubkey
}
