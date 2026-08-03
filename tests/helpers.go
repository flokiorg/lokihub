package tests

import (
	"crypto/rand"
	"encoding/hex"
	"testing"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/stretchr/testify/require"
)

// RandomHex32 returns a random 64-character lowercase hex string (32 random bytes).
func RandomHex32() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// FundApp inserts a settled incoming transaction so the app has a balance.
// paymentHash must be unique within a test to avoid DB UNIQUE constraint errors.
func FundApp(svc *TestService, appID uint, amountMloki uint64, paymentHash string) {
	svc.DB.Create(&db.Transaction{
		AppId:       &appID,
		State:       constants.TRANSACTION_STATE_SETTLED,
		Type:        constants.TRANSACTION_TYPE_INCOMING,
		AmountMloki: amountMloki,
		PaymentHash: paymentHash,
	})
}

// CreateCashHub creates a cash_hub app with the given per-wallet limits.
func CreateCashHub(t *testing.T, svc *TestService, perWalletMaxMloki, maxExpSecs int) *db.App {
	t.Helper()
	hub, _, err := svc.AppsService.CreateCashHub(
		"test-hub", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.CASH_HUB_SCOPE, constants.PAY_INVOICE_SCOPE, constants.GET_BALANCE_SCOPE},
		nil,
		db.CashHubConfig{PerWalletMaxMloki: perWalletMaxMloki, MaxExpSecs: maxExpSecs},
	)
	require.NoError(t, err)
	return hub
}
