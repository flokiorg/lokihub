package nip47

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ohstr/nmilat/nipcash"
	"github.com/ohstr/nmilat/nipcash/transport"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/tests"
)

// NIP-CASH §Bearer Items: an item MUST NOT carry both a slice proof and a cash
// secret (CASH-cash-mode-slices-46). It comes from a sender who holds the bill,
// so the Hub names the mistake rather than serving it either way.
func TestPrivateItem_ProofAndCashSecretTogether_Refused(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	nip47svc := NewNip47Service(svc.DB, svc.Cfg, svc.Keys, svc.EventPublisher, nil)
	walletPubkey, recipientPriv, _, connPriv := privateDispatchFixture(t, svc)

	hubXOnly := tests.RandomHex32()
	nonce, err := transport.NewNonce()
	require.NoError(t, err)
	notAfter := time.Now().Add(time.Minute).Unix()
	item := transport.Item{
		ID: "mixed", Target: walletPubkey, Method: nipcash.MethodCashStatus,
		Params: json.RawMessage(`{"cash_secret":"` + strings.Repeat("11", 32) + `","scope":"mine"}`),
	}
	binding := billProofBinding(t, item, hubXOnly, nonce, notAfter)
	item.Proof, err = transport.BuildItemProof(recipientPriv, binding) // the slice's real owner
	require.NoError(t, err)
	item.BillProof, err = transport.BuildBillProof(connPriv, binding)
	require.NoError(t, err)
	require.True(t, item.HasProof() && item.IsBearer(), "the fixture must carry both")

	res, served := nip47svc.ServePrivateItem(context.TODO(), nil, item,
		PrivateItemBinding{HubXOnly: hubXOnly, Nonce: nonce, NotAfter: notAfter})
	require.True(t, served)
	require.NotNil(t, res.Error, "an item asserting both must not be served: %s", string(res.Result))
	require.Equal(t, constants.ERROR_BAD_REQUEST, res.Error.Code)
	require.Nil(t, res.Result)
}
