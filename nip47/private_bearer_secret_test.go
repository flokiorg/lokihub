package nip47

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ohstr/nmilat/nipcash/transport"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

// NIP-CASH I8: a cash_status item carrying a cash_secret is refused BAD_REQUEST. A
// cash-mode cash_status is answered from the bill proof alone, so a secret in it
// is a client error that would put the secret where the roster read does not need
// it. nmilat refuses the shape when the envelope is decoded.
func TestPrivateItem_ProoflessCashStatus_SecretIsRefused(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	nip47svc := NewNip47Service(svc.DB, svc.Cfg, svc.Keys, svc.EventPublisher, nil)
	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	tests.FundApp(svc, hub.ID, 10_000_000, "fundtx")

	secret, commitment, err := tests.GenerateCashSecret()
	require.NoError(t, err)
	walletPubkey, connPriv, walletID := auditBBill(t, svc, hub.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityCash, IdentityValue: commitment, AmountMloki: 4200},
	})

	hubXOnly := strings.Repeat("ab", 32)
	nonce := strings.Repeat("ef", 32)
	notAfter := time.Now().Add(time.Minute).Unix()

	// Even the RIGHT secret is refused: the roster read never needs it.
	params := json.RawMessage(`{"cash_secret":"` + secret + `","scope":"all"}`)
	hash, err := transport.CanonicalParamsHash(params)
	require.NoError(t, err)
	billProof, err := transport.BuildBillProof(connPriv, transport.ProofBinding{Target: walletPubkey,
		HubXOnly: hubXOnly, Method: constants.NIP47MethodCashStatus, ParamsHash: hash, Nonce: nonce, NotAfter: notAfter})
	require.NoError(t, err)
	item := transport.Item{ID: "w", Target: walletPubkey, Method: constants.NIP47MethodCashStatus,
		Params: params, BillProof: billProof}

	res, served := nip47svc.ServePrivateItem(context.TODO(), svc.LNClient, item,
		PrivateItemBinding{HubXOnly: hubXOnly, Nonce: nonce, NotAfter: notAfter})
	require.True(t, served)
	require.NotNil(t, res.Error)
	require.Equal(t, constants.ERROR_BAD_REQUEST, res.Error.Code)
	require.Nil(t, res.Result)
	var events int64
	require.NoError(t, svc.DB.Model(&db.RequestEvent{}).Where("nostr_id = ?", nonce+":w").Count(&events).Error)
	require.Zero(t, events)
	_ = walletID
}
