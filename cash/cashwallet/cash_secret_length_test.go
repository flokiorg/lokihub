package cashwallet

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

// U-5.4: a cash-mode recipient carries the caller's commitment; the Hub stores and
// echoes it and never sees or returns a secret (NIP-CASH §Minting Cash).
func TestCreate_Cash_StoresAndEchoesCallerCommitment(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	tests.FundApp(svc, hub.ID, 10_000_000, "fundtxhash")

	recipients := oneCashRecipient(1000)
	result, err := Create(context.TODO(), newTestDeps(svc), Params{
		HubApp: hub, Recipients: recipients, ExpirySecs: 1800,
	})
	require.NoError(t, err)
	require.Len(t, result.Recipients, 1)
	assert.Equal(t, recipients[0].IdentityValue, result.Recipients[0].IdentityValue, "the commitment is echoed")

	var slice db.CashWalletClaim
	require.NoError(t, svc.DB.Where("wallet_app_id = ?", result.WalletApp.ID).First(&slice).Error)
	assert.Equal(t, recipients[0].IdentityValue, slice.IdentityValue, "only the commitment is stored")

	body, err := json.Marshal(result.Recipients)
	require.NoError(t, err)
	assert.NotContains(t, strings.ToLower(string(body)), "secret")
}

// A cash entry whose identity_value is not a 64-hex commitment, or that names an
// IA, is refused before anything is created.
func TestCreate_Cash_RefusesMalformedCommitment(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	tests.FundApp(svc, hub.ID, 10_000_000, "fundtxhash")

	for name, r := range map[string]RecipientInput{
		"empty":     {IdentityType: db.CashIdentityCash, AmountMloki: 1000},
		"short":     {IdentityType: db.CashIdentityCash, IdentityValue: "abcd", AmountMloki: 1000},
		"uppercase": {IdentityType: db.CashIdentityCash, IdentityValue: strings.Repeat("AB", 32), AmountMloki: 1000},
		"withIA":    {IdentityType: db.CashIdentityCash, IdentityValue: strings.Repeat("ab", 32), IAPubkey: strings.Repeat("cd", 32), AmountMloki: 1000},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Create(context.TODO(), newTestDeps(svc), Params{HubApp: hub, Recipients: []RecipientInput{r}})
			require.ErrorIs(t, err, constants.ErrInvalidParams)
		})
	}
	var bills int64
	require.NoError(t, svc.DB.Model(&db.App{}).Where("parent_app_id = ?", hub.ID).Count(&bills).Error)
	assert.Zero(t, bills)
}
