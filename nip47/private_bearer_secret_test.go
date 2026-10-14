package nip47

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

// NIP-CASH §Bearer Items: "a Hub presented with a proofless item MUST look the target up,
// and serve it only if that slice is genuinely cash-mode and the supplied secret matches."
// A wrong secret clears the envelope gate (nmilat only asks that cash_secret be non-empty),
// so the hub itself has to compare it.
func TestPrivateItem_ProoflessCashStatus_WrongSecretIsNotServed(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	nip47svc := NewNip47Service(svc.DB, svc.Cfg, svc.Keys, svc.EventPublisher, nil)
	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	tests.FundApp(svc, hub.ID, 10_000_000, "fundtx")

	realSecret := strings.Repeat("11", 32)
	sb, _ := hex.DecodeString(realSecret)
	commit := sha256.Sum256(sb)
	walletPubkey, connPriv, _ := auditBBill(t, svc, hub.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityCash, IdentityValue: hex.EncodeToString(commit[:]), AmountMloki: 4200},
	})

	hubXOnly := strings.Repeat("ab", 32)
	nonce := strings.Repeat("ef", 32)
	notAfter := time.Now().Add(time.Minute).Unix()

	wrongSecret := strings.Repeat("22", 32)
	params := json.RawMessage(`{"cash_secret":"` + wrongSecret + `","scope":"all"}`)
	hash, err := transport.CanonicalParamsHash(params)
	require.NoError(t, err)
	billProof, err := transport.BuildBillProof(connPriv, transport.ProofBinding{Target: walletPubkey,
		HubXOnly: hubXOnly, Method: constants.NIP47MethodCashStatus, ParamsHash: hash, Nonce: nonce, NotAfter: notAfter})
	require.NoError(t, err)
	item := transport.Item{ID: "w", Target: walletPubkey, Method: constants.NIP47MethodCashStatus,
		Params: params, BillProof: billProof}

	env := transport.Envelope{Version: 1, NotAfter: notAfter, Nonce: nonce,
		ReplyTo: strings.Repeat("cd", 32), Items: []transport.Item{item}}
	plain, err := env.Encode(transport.DefaultLimits())
	require.NoError(t, err)
	_, err = transport.Decode(plain, transport.DefaultLimits())
	require.NoError(t, err, "the item clears the envelope gate")

	res, served := nip47svc.ServePrivateItem(context.TODO(), svc.LNClient, item,
		PrivateItemBinding{HubXOnly: hubXOnly, Nonce: nonce, NotAfter: notAfter})
	require.True(t, served)
	require.NotNil(t, res.Error, "a wrong cash_secret must not be served: %s", string(res.Result))
	require.Equal(t, constants.ERROR_NOT_FOUND, res.Error.Code)
	require.Nil(t, res.Result)
}
