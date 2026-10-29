package nip47

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ohstr/nmilat/nipcash/transport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

// An envelope with more than eight items that failed authorization runs no more
// value items: they are RATE_LIMITED. The bound is the envelope's, never a bill's.
func TestPrivateItem_EnvelopeAuthFailuresBoundValueItems(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	nip47svc := NewNip47Service(svc.DB, svc.Cfg, svc.Keys, svc.EventPublisher, nil)
	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	tests.FundApp(svc, hub.ID, 10_000_000, "fundtx")
	_, commitment := cashSecretPair(t)
	walletPubkey, connPriv, _ := auditBBill(t, svc, hub.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityCash, IdentityValue: commitment, AmountMloki: 4200},
	})

	hubXOnly := strings.Repeat("ab", 32)
	nonce := strings.Repeat("ef", 32)
	notAfter := time.Now().Add(time.Minute).Unix()
	failures := 0
	binding := PrivateItemBinding{HubXOnly: hubXOnly, Nonce: nonce, NotAfter: notAfter, AuthFailures: &failures}

	serve := func(id string) *transport.ResultError {
		params := json.RawMessage(`{"cash_secret":"` + strings.Repeat("22", 32) + `","invoice":"lnfc1"}`)
		hash, err := transport.CanonicalParamsHash(params)
		require.NoError(t, err)
		billProof, err := transport.BuildBillProof(connPriv, transport.ProofBinding{Target: walletPubkey,
			HubXOnly: hubXOnly, Method: constants.NIP47MethodCashRedeem, ParamsHash: hash, Nonce: nonce, NotAfter: notAfter})
		require.NoError(t, err)
		item := transport.Item{ID: id, Target: walletPubkey, Method: constants.NIP47MethodCashRedeem, Params: params, BillProof: billProof}
		res, served := nip47svc.ServePrivateItem(context.TODO(), svc.LNClient, item, binding)
		require.True(t, served)
		return res.Error
	}

	for i := 0; i <= maxEnvelopeAuthFailures; i++ {
		e := serve("i" + string(rune('a'+i)))
		require.NotNil(t, e)
		assert.Equal(t, constants.ERROR_UNAUTHORIZED, e.Code, "item %d", i)
	}
	e := serve("last")
	require.NotNil(t, e)
	assert.Equal(t, constants.ERROR_RATE_LIMITED, e.Code)
}
