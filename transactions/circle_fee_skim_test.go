package transactions

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/db/queries"
	"github.com/flokiorg/lokihub/tests"
)

// newCircleHub creates a circle_hub app with the given forwarding-fee rate
// (parts per million). PerWalletMaxMloki/MaxExpSecs are set high enough to
// never bind in these tests, which exercise SendPaymentSync/SendKeysend
// directly rather than the create_circle_wallet flow.
func newCircleHub(t *testing.T, svc *tests.TestService, feesPpm int) *db.App {
	t.Helper()
	hub, _, err := svc.AppsService.CreateCircleHub(
		"circle-hub", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.CIRCLE_WALLET_SCOPE, constants.GET_BALANCE_SCOPE},
		nil,
		apps.CircleIdentityRef{Name: "circle-hub-identity", Policy: db.CirclePolicyAllowlist},
		db.CircleHubConfig{
			MaxExpSecs:        3600,
			FeesPpm:           feesPpm,
			PerWalletMaxMloki: 10_000_000,
			MinBudgetRenewal:  constants.BUDGET_RENEWAL_NEVER,
		},
	)
	require.NoError(t, err)
	return hub
}

// newCircleWallet creates a circle_wallet child of hub with PAY_INVOICE_SCOPE,
// optionally budget-capped (maxAmountLoki=0 means unbounded).
func newCircleWallet(t *testing.T, svc *tests.TestService, hub *db.App, maxAmountLoki uint64, budgetRenewal string) *db.App {
	t.Helper()
	wallet, _, err := svc.AppsService.CreateApp(
		"circle-wallet", "", maxAmountLoki, budgetRenewal, nil,
		[]string{constants.PAY_INVOICE_SCOPE}, db.AppKindCircleWallet, &hub.ID, db.ParentKindCircle, nil,
	)
	require.NoError(t, err)
	return wallet
}

func TestSendPaymentSync_CircleWallet_FeeSkim_HappyPath(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	// 1% (10,000 ppm) forwarding fee.
	hub := newCircleHub(t, svc, 10_000)
	wallet := newCircleWallet(t, svc, hub, 0, constants.BUDGET_RENEWAL_NEVER)

	// tests.MockInvoice pays 123,000 mloki. Fee reserve = max(1%, 10_000) = 10_000.
	// Skim = 123_000 * 10_000 / 1_000_000 = 1_230.
	tests.FundApp(svc, wallet.ID, 140_000, tests.RandomHex32())

	transactionsService := NewTransactionsService(svc.DB, svc.EventPublisher)
	transaction, err := transactionsService.SendPaymentSync(tests.MockInvoice, nil, nil, svc.LNClient, &wallet.ID, nil)
	require.NoError(t, err)
	require.NotNil(t, transaction)

	assert.Equal(t, uint64(1_230), transaction.FeeSkimMloki)
	assert.Equal(t, constants.TRANSACTION_STATE_SETTLED, transaction.State)
	assert.Zero(t, transaction.FeeMloki) // MockLn.SendPaymentSync's default response has no fee

	walletBalance := queries.GetIsolatedBalance(svc.DB, wallet.ID)
	assert.Equal(t, int64(140_000-123_000-1_230), walletBalance)

	hubBalance := queries.GetIsolatedBalance(svc.DB, hub.ID)
	assert.Equal(t, int64(1_230), hubBalance)

	// The hub's credit is a distinct ledger row from the child's own payment:
	// its own synthetic payment hash, settled, tagged with its source.
	var hubCredit db.Transaction
	require.NoError(t, svc.DB.Where("app_id = ?", hub.ID).First(&hubCredit).Error)
	assert.Equal(t, constants.TRANSACTION_TYPE_INCOMING, hubCredit.Type)
	assert.Equal(t, constants.TRANSACTION_STATE_SETTLED, hubCredit.State)
	assert.Equal(t, uint64(1_230), hubCredit.AmountMloki)
	assert.NotEqual(t, tests.MockPaymentHash, hubCredit.PaymentHash)

	var metadata map[string]interface{}
	require.NoError(t, json.Unmarshal(hubCredit.Metadata, &metadata))
	assert.EqualValues(t, wallet.ID, metadata["circle_fee_skim_source_app_id"])
	assert.Equal(t, tests.MockPaymentHash, metadata["circle_fee_skim_source_payment_hash"])
}

func TestSendPaymentSync_CircleWallet_FeeSkim_ZeroPpm_NoSkim(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := newCircleHub(t, svc, 0)
	wallet := newCircleWallet(t, svc, hub, 0, constants.BUDGET_RENEWAL_NEVER)
	tests.FundApp(svc, wallet.ID, 140_000, tests.RandomHex32())

	transactionsService := NewTransactionsService(svc.DB, svc.EventPublisher)
	transaction, err := transactionsService.SendPaymentSync(tests.MockInvoice, nil, nil, svc.LNClient, &wallet.ID, nil)
	require.NoError(t, err)

	assert.Zero(t, transaction.FeeSkimMloki)
	assert.Zero(t, queries.GetIsolatedBalance(svc.DB, hub.ID))

	var count int64
	svc.DB.Model(&db.Transaction{}).Where("app_id = ?", hub.ID).Count(&count)
	assert.Zero(t, count, "no hub credit row should be created when FeesPpm is 0")
}

func TestSendPaymentSync_CircleWallet_FeeSkim_InsufficientBalance_IncludesSkim(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := newCircleHub(t, svc, 10_000) // 1%
	wallet := newCircleWallet(t, svc, hub, 0, constants.BUDGET_RENEWAL_NEVER)

	// Exactly enough for amount + fee reserve (123_000 + 10_000 = 133_000) — the
	// threshold that lets a plain isolated app succeed (see
	// TestSendPaymentSync_IsolatedApp_BalanceSufficient) — but a circle_wallet
	// also owes its hub a 1_230 mloki skim, so this same balance must be rejected.
	tests.FundApp(svc, wallet.ID, 133_000, tests.RandomHex32())

	transactionsService := NewTransactionsService(svc.DB, svc.EventPublisher)
	transaction, err := transactionsService.SendPaymentSync(tests.MockInvoice, nil, nil, svc.LNClient, &wallet.ID, nil)

	assert.Error(t, err)
	assert.ErrorIs(t, err, NewInsufficientBalanceError())
	assert.Nil(t, transaction)

	// Nothing was skimmed — the payment never happened.
	assert.Zero(t, queries.GetIsolatedBalance(svc.DB, hub.ID))
}

func TestSendPaymentSync_CircleWallet_FeeSkim_QuotaExceeded(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := newCircleHub(t, svc, 10_000) // 1%
	// Budget cap of 133 loki covers amount+feeReserve exactly (123_000 + 10_000
	// mloki = 133 loki) but not the extra 1_230 mloki (1.23 loki) skim.
	wallet := newCircleWallet(t, svc, hub, 133, constants.BUDGET_RENEWAL_NEVER)
	tests.FundApp(svc, wallet.ID, 500_000, tests.RandomHex32())

	transactionsService := NewTransactionsService(svc.DB, svc.EventPublisher)
	transaction, err := transactionsService.SendPaymentSync(tests.MockInvoice, nil, nil, svc.LNClient, &wallet.ID, nil)

	assert.Error(t, err)
	assert.ErrorIs(t, err, NewQuotaExceededError())
	assert.Nil(t, transaction)
}

// TestSendPaymentSync_CircleWallet_FeeSkim_SelfPayment_NotSkimmed covers the
// product decision that any same-instance payment is fee-free, not just the
// hub's own internal_transfer reclaim: every JIT wallet, circle wallet, and
// generic NWC app hosted by this same lokihub instance settles between each
// other via the self-payment shortcut (selfPayment=true, hit whenever the
// invoice being paid was minted by an app on this instance — see
// integration/README.md) — that flag alone is exactly "member-to-member /
// member-to-any-other-app-on-this-instance" traffic, which stays fee-free
// unconditionally. Only a payment that genuinely leaves this instance over
// the real Lightning network is skimmable. Run with and without the
// internal_transfer metadata flag to prove the flag itself is irrelevant to
// this decision (it only matters for enforceJITFullDrain, JIT-only).
func TestSendPaymentSync_CircleWallet_FeeSkim_SelfPayment_NotSkimmed(t *testing.T) {
	for _, tc := range []struct {
		name     string
		metadata map[string]interface{}
	}{
		{name: "WithInternalTransferFlag", metadata: map[string]interface{}{"internal_transfer": true}},
		{name: "WithoutInternalTransferFlag", metadata: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, err := tests.CreateTestService(t)
			require.NoError(t, err)
			defer svc.Remove()

			svc.LNClient.(*tests.MockLn).Pubkey = "03cbd788f5b22bd56e2714bff756372d2293504c064e03250ed16a4dd80ad70e2c"

			hub := newCircleHub(t, svc, 500_000) // 50% — would be unmistakable if wrongly skimmed
			wallet := newCircleWallet(t, svc, hub, 0, constants.BUDGET_RENEWAL_NEVER)
			tests.FundApp(svc, wallet.ID, 200_000, tests.RandomHex32())

			// An existing incoming transaction with the same payment hash makes
			// SendPaymentSync detect selfPayment=true (see
			// self_payment_detection_test.go for the base mechanism).
			mockPreimage := "123preimage"
			require.NoError(t, svc.DB.Create(&db.Transaction{
				State:          constants.TRANSACTION_STATE_PENDING,
				Type:           constants.TRANSACTION_TYPE_INCOMING,
				PaymentRequest: tests.MockInvoice,
				PaymentHash:    tests.MockPaymentHash,
				Preimage:       &mockPreimage,
				AmountMloki:    123_000,
			}).Error)

			transactionsService := NewTransactionsService(svc.DB, svc.EventPublisher)
			transaction, err := transactionsService.SendPaymentSync(
				tests.MockInvoice, nil, tc.metadata, svc.LNClient, &wallet.ID, nil,
			)
			require.NoError(t, err)
			require.True(t, transaction.SelfPayment)
			assert.Zero(t, transaction.FeeSkimMloki)
			assert.Zero(t, queries.GetIsolatedBalance(svc.DB, hub.ID))
		})
	}
}

func TestSendKeysend_CircleWallet_FeeSkim(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := newCircleHub(t, svc, 100_000) // 10%
	wallet := newCircleWallet(t, svc, hub, 0, constants.BUDGET_RENEWAL_NEVER)
	// amount(10_000) + feeReserve(max(1%,10_000)=10_000) + skim(1_000) = 21_000
	tests.FundApp(svc, wallet.ID, 25_000, tests.RandomHex32())

	transactionsService := NewTransactionsService(svc.DB, svc.EventPublisher)
	transaction, err := transactionsService.SendKeysend(10_000, "some-destination", nil, "", svc.LNClient, &wallet.ID, nil)
	require.NoError(t, err)

	assert.Equal(t, uint64(1_000), transaction.FeeSkimMloki)
	assert.Equal(t, int64(1_000), queries.GetIsolatedBalance(svc.DB, hub.ID))
}

func TestSendPaymentSync_CircleWallet_NoParentConfig_NoSkim(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	// A circle_wallet whose parent app has no circle_hub_configs row at all
	// (e.g. an orphaned/malformed lineage) must not error — the LEFT JOIN in
	// validateCanPay simply yields fees_ppm = 0.
	parent := &db.App{Name: "parent", Kind: db.AppKindCircleHub}
	require.NoError(t, svc.DB.Create(parent).Error)

	wallet := &db.App{Name: "wallet", Kind: db.AppKindCircleWallet, ParentAppID: &parent.ID, ParentKind: db.ParentKindCircle}
	require.NoError(t, svc.DB.Create(wallet).Error)
	require.NoError(t, svc.DB.Create(&db.AppPermission{AppId: wallet.ID, App: *wallet, Scope: constants.PAY_INVOICE_SCOPE}).Error)

	tests.FundApp(svc, wallet.ID, 140_000, tests.RandomHex32())

	transactionsService := NewTransactionsService(svc.DB, svc.EventPublisher)
	transaction, err := transactionsService.SendPaymentSync(tests.MockInvoice, nil, nil, svc.LNClient, &wallet.ID, nil)
	require.NoError(t, err)
	assert.Zero(t, transaction.FeeSkimMloki)
}

func TestCalculateFeeSkimMloki(t *testing.T) {
	assert.Equal(t, uint64(0), CalculateFeeSkimMloki(100_000, 0))
	assert.Equal(t, uint64(0), CalculateFeeSkimMloki(100_000, -500), "negative ppm must never skim")
	assert.Equal(t, uint64(1_000), CalculateFeeSkimMloki(100_000, 10_000), "1%")
	assert.Equal(t, uint64(100_000), CalculateFeeSkimMloki(100_000, 1_000_000), "100% ceiling skims the whole amount")
	assert.Equal(t, uint64(0), CalculateFeeSkimMloki(1, 500_000), "sub-unit skims floor to zero, never round up")
	assert.Equal(t, uint64(1), CalculateFeeSkimMloki(19, 100_000), "19 * 0.1 = 1.9, floors to 1")
	assert.Equal(t, uint64(0), CalculateFeeSkimMloki(0, 1_000_000), "zero amount always skims zero")
}
