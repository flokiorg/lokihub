package transactions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/atrest"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/tests"
)

// sealedPreimage opens the sealed preimage of a circle invoice row, checking it
// is the preimage of the row's hash.
func sealedPreimage(t *testing.T, svc *tests.TestService, id uint) string {
	t.Helper()
	row := rowState(t, svc, id)
	require.NotNil(t, row.HoldPreimageSealed)
	preimage, err := atrest.Open(*row.HoldPreimageSealed)
	require.NoError(t, err)
	raw, err := hex.DecodeString(preimage)
	require.NoError(t, err)
	sum := sha256.Sum256(raw)
	require.Equal(t, row.PaymentHash, hex.EncodeToString(sum[:]))
	return preimage
}

// A circle wallet's invoice keeps its preimage sealed, never plain, until it
// settles; the settlement stores the preimage plain (the payer's proof) and
// drops the sealed copy.
func TestCircleInvoice_PreimageIsSealedUntilSettled(t *testing.T) {
	svc, ts, wallet := holdCircle(t)
	ts.SetLNClientProvider(func() lnclient.LNClient { return svc.LNClient })
	invoice, err := ts.MakeCircleInvoice(context.TODO(), wallet, 20_000, "", "", 3600, nil, svc.LNClient, nil)
	require.NoError(t, err)
	assert.Nil(t, invoice.Preimage, "the member's reply carries no preimage")
	stored := rowState(t, svc, invoice.ID)
	assert.Nil(t, stored.Preimage, "nothing plain at rest")
	require.NotNil(t, stored.HoldPreimageSealed)
	preimage := sealedPreimage(t, svc, invoice.ID)
	assert.NotContains(t, *stored.HoldPreimageSealed, preimage)

	acceptExternally(t, ts, invoice.PaymentHash, 20_000)
	settled := rowState(t, svc, invoice.ID)
	assert.Equal(t, constants.TRANSACTION_STATE_SETTLED, settled.State)
	require.NotNil(t, settled.Preimage)
	assert.Equal(t, preimage, *settled.Preimage)
	assert.Nil(t, settled.HoldPreimageSealed)
}

// Rows written before sealing are sealed once the key is loaded: an open
// circle invoice's preimage is sealed and still settles; a settled one, an
// ordinary invoice and a member's own hold invoice are left as they are.
// Running it again changes nothing.
func TestSealHoldPreimages_SealsOnlyOpenCircleHolds(t *testing.T) {
	svc, ts, wallet := holdCircle(t)
	ts.SetLNClientProvider(func() lnclient.LNClient { return svc.LNClient })
	invoice, err := ts.MakeCircleInvoice(context.TODO(), wallet, 20_000, "", "", 3600, nil, svc.LNClient, nil)
	require.NoError(t, err)
	preimage := sealedPreimage(t, svc, invoice.ID)
	// As written before: plain, unsealed.
	require.NoError(t, svc.DB.Model(&db.Transaction{}).Where("id = ?", invoice.ID).
		Updates(map[string]interface{}{"preimage": preimage, "hold_preimage_sealed": nil}).Error)

	plain := strings.Repeat("ab", 32)
	settledCircle := db.Transaction{AppId: &wallet.ID, Type: constants.TRANSACTION_TYPE_INCOMING, State: constants.TRANSACTION_STATE_SETTLED,
		Hold: true, PaymentHash: tests.RandomHex32(), Preimage: &plain}
	other, _, err := tests.CreateApp(svc)
	require.NoError(t, err)
	ordinary := db.Transaction{AppId: &other.ID, Type: constants.TRANSACTION_TYPE_INCOMING, State: constants.TRANSACTION_STATE_PENDING,
		PaymentHash: tests.RandomHex32(), Preimage: &plain}
	memberHold := db.Transaction{AppId: &other.ID, Type: constants.TRANSACTION_TYPE_INCOMING, State: constants.TRANSACTION_STATE_PENDING,
		Hold: true, PaymentHash: tests.RandomHex32(), Preimage: &plain}
	for _, r := range []*db.Transaction{&settledCircle, &ordinary, &memberHold} {
		require.NoError(t, svc.DB.Create(r).Error)
	}

	n, err := SealHoldPreimages(svc.DB)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Nil(t, rowState(t, svc, invoice.ID).Preimage)
	assert.Equal(t, preimage, sealedPreimage(t, svc, invoice.ID))
	for _, r := range []db.Transaction{settledCircle, ordinary, memberHold} {
		got := rowState(t, svc, r.ID)
		require.NotNil(t, got.Preimage)
		assert.Equal(t, plain, *got.Preimage)
		assert.Nil(t, got.HoldPreimageSealed)
	}
	n, err = SealHoldPreimages(svc.DB)
	require.NoError(t, err)
	assert.Zero(t, n, "idempotent")

	acceptExternally(t, ts, invoice.PaymentHash, 20_000)
	assert.Equal(t, constants.TRANSACTION_STATE_SETTLED, rowState(t, svc, invoice.ID).State, "a migrated invoice still settles")
	assert.Contains(t, svc.LNClient.(*tests.MockLn).SettleHoldPreimages(), preimage)
}

// A sealed preimage opens only under the key that sealed it, and never before a
// key is loaded.
func TestAtRest_SealOpensOnlyUnderItsKey(t *testing.T) {
	_, _, _ = holdCircle(t) // loads this node's key
	sealed, err := atrest.Seal("secret")
	require.NoError(t, err)
	got, err := atrest.Open(sealed)
	require.NoError(t, err)
	assert.Equal(t, "secret", got)
	_, err = atrest.Open("secret")
	assert.Error(t, err, "a plain value is not sealed")

	atrest.SetKey(atrest.DeriveKey([]byte("another node")))
	_, err = atrest.Open(sealed)
	assert.Error(t, err, "another node's key opens nothing")
	atrest.SetKey(nil)
	_, err = atrest.Seal("x")
	assert.ErrorIs(t, err, atrest.ErrLocked)
}
