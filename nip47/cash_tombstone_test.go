package nip47

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/nip47/cipher"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/flokiorg/lokihub/tests"
	"github.com/ohstr/nmilat/nipcash"
)

// seedDestroyedBill writes the archive rows a spent bill leaves behind, and
// returns the wallet pubkey a caller would address it by.
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
		WalletAppID:   walletAppID,
		HubAppID:      hub.ID,
		WalletPubkey:  walletPubkey,
		EndedAt:       endedAt,
		RetainedUntil: retainedUntil,
		Outcome:       db.CashBillOutcomeDrained,
		TotalMloki:    1000,
		FundedMloki:   1000,
	}).Error)
	return walletAppID, walletPubkey
}

// cashStatusRequest builds a signed, encrypted cash_status request addressed to
// walletPubkey and signed by senderPrivKey.
func cashStatusRequest(t *testing.T, senderPrivKey, walletPubkey, method string) *nostr.Event {
	t.Helper()

	senderPubkey, err := nostr.GetPublicKey(senderPrivKey)
	require.NoError(t, err)

	c, err := cipher.NewNip47Cipher(constants.ENCRYPTION_TYPE_NIP44_V2, walletPubkey, senderPrivKey)
	require.NoError(t, err)
	payload, err := json.Marshal(map[string]interface{}{"method": method})
	require.NoError(t, err)
	content, err := c.Encrypt(string(payload))
	require.NoError(t, err)

	ev := &nostr.Event{
		Kind:      models.REQUEST_KIND,
		PubKey:    senderPubkey,
		CreatedAt: nostr.Now(),
		Tags: nostr.Tags{
			{"p", walletPubkey},
			{"encryption", constants.ENCRYPTION_TYPE_NIP44_V2},
		},
		Content: content,
	}
	require.NoError(t, ev.Sign(senderPrivKey))
	return ev
}

// The holder — whoever has the bill's own connection secret from its
// lokicash1... token — gets a definitive answer instead of a timeout.
func TestTombstone_Holder_GetsSpentAnswer(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	nip47svc := NewNip47Service(svc.DB, svc.Cfg, svc.Keys, svc.EventPublisher, nil)
	walletAppID, walletPubkey := seedDestroyedBill(t, svc, time.Now().Add(-time.Hour), 24*60*60)

	pairingKey, err := svc.Keys.GetCashPairingKey(walletAppID)
	require.NoError(t, err)

	pool := tests.NewMockSimplePool()
	nip47svc.HandleEvent(context.TODO(), pool,
		cashStatusRequest(t, pairingKey, walletPubkey, nipcash.MethodCashStatus), svc.LNClient)

	require.Len(t, pool.PublishedEvents, 1, "the bill's holder must get an answer, not silence")
}

// THE security gate. A destroyed bill's wallet pubkey is public — it travels in
// clear-text `p` tags on every request the bill ever served, so anyone watching
// a relay can harvest it. Answering whoever asks would turn the hub into a
// queryable index of every bill it ever issued, which is precisely what
// deleting the bill exists to prevent (NIP-CASH §Answering About a Destroyed
// Bill).
//
// Note the request below decrypts perfectly: under NIP-47 anyone may encrypt to
// a wallet pubkey with a freshly generated key, and the hub would encrypt its
// reply straight back to them. Decryption is NOT authentication, which is why
// this check cannot be skipped.
func TestTombstone_Stranger_GetsSilence(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	nip47svc := NewNip47Service(svc.DB, svc.Cfg, svc.Keys, svc.EventPublisher, nil)
	_, walletPubkey := seedDestroyedBill(t, svc, time.Now().Add(-time.Hour), 24*60*60)

	pool := tests.NewMockSimplePool()
	nip47svc.HandleEvent(context.TODO(), pool,
		cashStatusRequest(t, nostr.GeneratePrivateKey(), walletPubkey, nipcash.MethodCashStatus), svc.LNClient)

	require.Empty(t, pool.PublishedEvents,
		"a caller who does not hold this bill's connection must be met with silence — "+
			"answering makes the hub an existence oracle for every bill it issued")
}

// Past the window the hub returns to silence, so the tombstone is never wrong,
// only absent for old bills.
func TestTombstone_PastRetention_GetsSilence(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	nip47svc := NewNip47Service(svc.DB, svc.Cfg, svc.Keys, svc.EventPublisher, nil)
	walletAppID, walletPubkey := seedDestroyedBill(t, svc, time.Now().Add(-48*time.Hour), 24*60*60)

	pairingKey, err := svc.Keys.GetCashPairingKey(walletAppID)
	require.NoError(t, err)

	pool := tests.NewMockSimplePool()
	nip47svc.HandleEvent(context.TODO(), pool,
		cashStatusRequest(t, pairingKey, walletPubkey, nipcash.MethodCashStatus), svc.LNClient)

	require.Empty(t, pool.PublishedEvents, "past retained_until the hub is silent again")
}

// Retention 0 disables the tombstone entirely: the hub falls silent the moment
// a bill is destroyed, which is the behaviour that predates this feature.
func TestTombstone_RetentionDisabled_GetsSilence(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	nip47svc := NewNip47Service(svc.DB, svc.Cfg, svc.Keys, svc.EventPublisher, nil)
	walletAppID, walletPubkey := seedDestroyedBill(t, svc, time.Now(), 0)

	pairingKey, err := svc.Keys.GetCashPairingKey(walletAppID)
	require.NoError(t, err)

	pool := tests.NewMockSimplePool()
	nip47svc.HandleEvent(context.TODO(), pool,
		cashStatusRequest(t, pairingKey, walletPubkey, nipcash.MethodCashStatus), svc.LNClient)

	require.Empty(t, pool.PublishedEvents, "retention 0 means no tombstone at all")
}

// Only the status read answers. cash_redeem and cash_transfer against a
// destroyed bill keep their silence: a status read asks about existence, those
// attempt to move value, and answering them widens what a past holder of a
// spent secret can confirm.
func TestTombstone_OtherMethods_StaySilent(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	nip47svc := NewNip47Service(svc.DB, svc.Cfg, svc.Keys, svc.EventPublisher, nil)
	walletAppID, walletPubkey := seedDestroyedBill(t, svc, time.Now().Add(-time.Hour), 24*60*60)

	pairingKey, err := svc.Keys.GetCashPairingKey(walletAppID)
	require.NoError(t, err)

	for _, method := range []string{
		nipcash.MethodCashRedeem,
		nipcash.MethodCashTransfer,
		nipcash.MethodCashConsolidate,
	} {
		t.Run(method, func(t *testing.T) {
			pool := tests.NewMockSimplePool()
			nip47svc.HandleEvent(context.TODO(), pool,
				cashStatusRequest(t, pairingKey, walletPubkey, method), svc.LNClient)
			require.Empty(t, pool.PublishedEvents,
				"only cash_status answers about a destroyed bill")
		})
	}
}

// The deprecated wire name must reach the same tombstone, so a client that has
// not been updated still gets a definitive answer rather than a timeout.
func TestTombstone_DeprecatedMethodName_StillAnswers(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	nip47svc := NewNip47Service(svc.DB, svc.Cfg, svc.Keys, svc.EventPublisher, nil)
	walletAppID, walletPubkey := seedDestroyedBill(t, svc, time.Now().Add(-time.Hour), 24*60*60)

	pairingKey, err := svc.Keys.GetCashPairingKey(walletAppID)
	require.NoError(t, err)

	pool := tests.NewMockSimplePool()
	nip47svc.HandleEvent(context.TODO(), pool,
		cashStatusRequest(t, pairingKey, walletPubkey, nipcash.MethodListRecipients), svc.LNClient)

	require.Len(t, pool.PublishedEvents, 1, "the old method name must still be answered")
}
