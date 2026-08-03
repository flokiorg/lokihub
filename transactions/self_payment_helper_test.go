package transactions

// Direct coverage for the exported IsSelfPayment predicate, extracted out of
// SendPaymentSync's own inline self-payment check so cash_redeem_controller.go
// can peek at the same answer before it decides whether its redeem fee
// applies to a given redemption (see IsSelfPayment's own doc comment). This
// extraction is behavior-preserving — self_payment_detection_test.go's
// existing SendPaymentSync-level suite already proves that end-to-end;
// these tests instead exercise IsSelfPayment itself, directly, the same way
// cash_redeem_controller.go now calls it.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	decodepay "github.com/flokiorg/lokihub/decodepay"
	"github.com/flokiorg/lokihub/tests"
)

func TestIsSelfPayment_MatchingPubkeyAndIncomingTransaction_True(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	svc.LNClient.(*tests.MockLn).Pubkey = "03cbd788f5b22bd56e2714bff756372d2293504c064e03250ed16a4dd80ad70e2c"

	require.NoError(t, svc.DB.Create(&db.Transaction{
		Type:        constants.TRANSACTION_TYPE_INCOMING,
		PaymentHash: tests.MockPaymentHash,
		AmountMloki: 123_000,
	}).Error)

	paymentRequest, err := decodepay.Decode(tests.MockInvoice)
	require.NoError(t, err)

	assert.True(t, IsSelfPayment(svc.DB, paymentRequest, svc.LNClient))
}

func TestIsSelfPayment_MatchingPubkeyNoIncomingTransaction_False(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	svc.LNClient.(*tests.MockLn).Pubkey = "03cbd788f5b22bd56e2714bff756372d2293504c064e03250ed16a4dd80ad70e2c"

	paymentRequest, err := decodepay.Decode(tests.MockInvoice)
	require.NoError(t, err)

	assert.False(t, IsSelfPayment(svc.DB, paymentRequest, svc.LNClient),
		"a matching pubkey alone, with no corresponding incoming transaction row, is not enough")
}

func TestIsSelfPayment_DifferentPubkey_False(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	svc.LNClient.(*tests.MockLn).Pubkey = "a-completely-different-pubkey"

	require.NoError(t, svc.DB.Create(&db.Transaction{
		Type:        constants.TRANSACTION_TYPE_INCOMING,
		PaymentHash: tests.MockPaymentHash,
		AmountMloki: 123_000,
	}).Error)

	paymentRequest, err := decodepay.Decode(tests.MockInvoice)
	require.NoError(t, err)

	assert.False(t, IsSelfPayment(svc.DB, paymentRequest, svc.LNClient))
}
