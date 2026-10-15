package cashwallet

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

// NIP-CASH §Cash-Mode Slices: the Hub generates the cash_secret "with enough
// entropy that guessing it is infeasible". Uniqueness alone would still pass
// with a 1-byte secret, so the size is pinned here: 32 bytes, and the stored
// commitment is the SHA-256 of exactly those bytes.
func TestGenerateCashSecret_Is32BytesAndCommitsToThem(t *testing.T) {
	secretHex, hashHex, err := GenerateCashSecret()
	require.NoError(t, err)

	secret, err := hex.DecodeString(secretHex)
	require.NoError(t, err)
	assert.Len(t, secret, 32)

	want := sha256.Sum256(secret)
	assert.Equal(t, hex.EncodeToString(want[:]), hashHex)
}

func TestCreate_Cash_MintedSecretIs32Bytes(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	tests.FundApp(svc, hub.ID, 10_000_000, "fundtxhash")

	result, err := Create(context.TODO(), newTestDeps(svc), Params{
		HubApp: hub, Recipients: oneCashRecipient(1000), ExpirySecs: 1800,
	})
	require.NoError(t, err)
	require.Len(t, result.Recipients, 1)

	secret, err := hex.DecodeString(result.Recipients[0].CashSecret)
	require.NoError(t, err)
	assert.Len(t, secret, 32, "the secret a minted cash slice is handed out with")

	var slice db.CashWalletClaim
	require.NoError(t, svc.DB.Where("wallet_app_id = ?", result.WalletApp.ID).First(&slice).Error)
	commitment := sha256.Sum256(secret)
	assert.Equal(t, hex.EncodeToString(commitment[:]), slice.IdentityValue, "only the commitment is stored")
}
