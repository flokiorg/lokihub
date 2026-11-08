package nip47

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/ohstr/nmilat/nipcash/transport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/tests"
)

// A Pre-Authorisation grant (NIP-CASH §Pre-Authorisation) lets a bill holder
// delegate limited authority over a bill without handing over the bearer connection
// secret. The grant is signed by the bill's connection key (the same key that signs
// a bill proof) and the hub accepts it IN PLACE OF a bill proof — but only while the
// item's own slice proof is signed by the grant's named delegate. That binding is
// the one load-bearing rule; without it a leaked grant would be a second bearer
// secret, so the attack surface is tested directly, not inferred from the happy path.

// buildGrantItem assembles a grant item the way a delegate does: a slice proof
// signed by the delegate, and the grant in place of a bill proof.
func buildGrantItem(t *testing.T, id, target, method, params, delegatePriv string, grant json.RawMessage, hubXOnly, nonce string, notAfter int64) transport.Item {
	t.Helper()
	hash, err := transport.CanonicalParamsHash(json.RawMessage(params))
	require.NoError(t, err)
	proof, err := transport.BuildItemProof(delegatePriv, transport.ProofBinding{
		Target: target, HubXOnly: hubXOnly, Method: method,
		ParamsHash: hash, Nonce: nonce, NotAfter: notAfter,
	})
	require.NoError(t, err)
	return transport.Item{
		ID: id, Target: target, Method: method,
		Params: json.RawMessage(params), Proof: proof, Grant: grant,
	}
}

// delegateKeypair returns a fresh private key and its x-only pubkey for a delegate.
func delegateKeypair(t *testing.T) (priv, xonly string) {
	t.Helper()
	priv = nostr.GeneratePrivateKey()
	xonly, err := nostr.GetPublicKey(priv)
	require.NoError(t, err)
	return priv, xonly
}

// TestServePrivateItem_ServesAGrantDelegate is the happy path: a bill holder grants
// cash_status to a delegate (an accountant), who reads the bill though it holds no
// slice of its own.
func TestServePrivateItem_ServesAGrantDelegate(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	nip47svc := NewNip47Service(svc.DB, svc.Cfg, svc.Keys, svc.EventPublisher, nil)
	walletPubkey, _, _, connPriv := privateDispatchFixture(t, svc)

	hubXOnly := strings.Repeat("ab", 32)
	nonce := strings.Repeat("cd", 32)
	notAfter := time.Now().Add(time.Minute).Unix()
	binding := PrivateItemBinding{HubXOnly: hubXOnly, Nonce: nonce, NotAfter: notAfter}

	delegatePriv, delegatePub := delegateKeypair(t)
	grant, err := transport.BuildGrant(connPriv, transport.GrantParams{
		Delegate:  delegatePub,
		Target:    walletPubkey,
		HubXOnly:  hubXOnly,
		Method:    constants.NIP47MethodCashStatus,
		ExpiresAt: time.Now().Add(time.Hour).Unix(),
	})
	require.NoError(t, err)

	item := buildGrantItem(t, "g1", walletPubkey, constants.NIP47MethodCashStatus, `{"scope":"all"}`,
		delegatePriv, grant, hubXOnly, nonce, notAfter)

	result, served := nip47svc.ServePrivateItem(context.TODO(), svc.LNClient, item, binding)
	require.True(t, served, "a grant delegate reading cash_status must be served")
	assert.Equal(t, "g1", result.ID)
	assert.Nil(t, result.Error, "an authorized grant delegate must not get an error")
}

// TestServePrivateItem_GrantForgeriesAreOmitted is the attack surface. Every case is
// a caller who should NOT be able to act on the bill, and each must be met with
// silence (served=false) — never an error, because an error would tell a grant thief
// why their grant failed, turning the hub into an oracle.
func TestServePrivateItem_GrantForgeriesAreOmitted(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	nip47svc := NewNip47Service(svc.DB, svc.Cfg, svc.Keys, svc.EventPublisher, nil)
	walletPubkey, _, _, connPriv := privateDispatchFixture(t, svc)

	hubXOnly := strings.Repeat("ab", 32)
	nonce := strings.Repeat("cd", 32)
	notAfter := time.Now().Add(time.Minute).Unix()
	binding := PrivateItemBinding{HubXOnly: hubXOnly, Nonce: nonce, NotAfter: notAfter}

	// A valid grant for the delegate, reused as the base of each forgery.
	delegatePriv, delegatePub := delegateKeypair(t)
	validGrant := func(method string, expires int64, singleUse bool) json.RawMessage {
		g, err := transport.BuildGrant(connPriv, transport.GrantParams{
			Delegate: delegatePub, Target: walletPubkey, HubXOnly: hubXOnly,
			Method: method, ExpiresAt: expires, SingleUse: singleUse,
		})
		require.NoError(t, err)
		return g
	}
	future := time.Now().Add(time.Hour).Unix()

	for _, tc := range []struct {
		name string
		item func() transport.Item
		why  string
	}{
		{
			name: "slice proof signed by someone other than the delegate",
			item: func() transport.Item {
				impostorPriv, _ := delegateKeypair(t)
				return buildGrantItem(t, "x", walletPubkey, constants.NIP47MethodCashStatus, `{"scope":"all"}`,
					impostorPriv, validGrant(constants.NIP47MethodCashStatus, future, false), hubXOnly, nonce, notAfter)
			},
			why: "the load-bearing rule: a leaked grant is useless without the delegate's own key",
		},
		{
			name: "expired grant",
			item: func() transport.Item {
				past := time.Now().Add(-time.Minute).Unix()
				return buildGrantItem(t, "x", walletPubkey, constants.NIP47MethodCashStatus, `{"scope":"all"}`,
					delegatePriv, validGrant(constants.NIP47MethodCashStatus, past, false), hubXOnly, nonce, notAfter)
			},
			why: "a grant is bounded by its own expiry",
		},
		{
			name: "grant signed by a key that does not hold the bill",
			item: func() transport.Item {
				strangerPriv, _ := delegateKeypair(t)
				g, err := transport.BuildGrant(strangerPriv, transport.GrantParams{
					Delegate: delegatePub, Target: walletPubkey, HubXOnly: hubXOnly,
					Method: constants.NIP47MethodCashStatus, ExpiresAt: future,
				})
				require.NoError(t, err)
				return buildGrantItem(t, "x", walletPubkey, constants.NIP47MethodCashStatus, `{"scope":"all"}`,
					delegatePriv, g, hubXOnly, nonce, notAfter)
			},
			why: "only the bill's connection key may delegate authority over it",
		},
		{
			name: "grant for a value method (pull payment, not yet served)",
			item: func() transport.Item {
				return buildGrantItem(t, "x", walletPubkey, constants.NIP47MethodCashRedeem, `{"invoice":"lnbc1"}`,
					delegatePriv, validGrant(constants.NIP47MethodCashRedeem, future, false), hubXOnly, nonce, notAfter)
			},
			why: "value methods under a grant need the amount-cap store, not yet built",
		},
		{
			name: "both a grant and a bill proof",
			item: func() transport.Item {
				it := buildGrantItem(t, "x", walletPubkey, constants.NIP47MethodCashStatus, `{"scope":"all"}`,
					delegatePriv, validGrant(constants.NIP47MethodCashStatus, future, false), hubXOnly, nonce, notAfter)
				bp, err := transport.BuildBillProof(connPriv, transport.ProofBinding{
					Target: walletPubkey, HubXOnly: hubXOnly, Method: constants.NIP47MethodCashStatus,
					ParamsHash: mustHash(t, it.Params), Nonce: nonce, NotAfter: notAfter,
				})
				require.NoError(t, err)
				it.BillProof = bp
				return it
			},
			why: "a grant stands in place of a bill proof; carrying both is malformed",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, served := nip47svc.ServePrivateItem(context.TODO(), svc.LNClient, tc.item(), binding)
			assert.False(t, served, "must be omitted (%s): %s", tc.why, tc.name)
			assert.Nil(t, result.Error, "an omission carries no error result: %s", tc.name)
		})
	}
}

// TestServePrivateItem_SingleUseGrantSpentOnce: a single-use grant is honoured
// exactly once. The second request carrying the same grant — even with its own
// fresh slice proof in a new envelope — is answered with silence.
func TestServePrivateItem_SingleUseGrantSpentOnce(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	nip47svc := NewNip47Service(svc.DB, svc.Cfg, svc.Keys, svc.EventPublisher, nil)
	walletPubkey, _, _, connPriv := privateDispatchFixture(t, svc)

	hubXOnly := strings.Repeat("ab", 32)
	notAfter := time.Now().Add(time.Minute).Unix()

	delegatePriv, delegatePub := delegateKeypair(t)
	grant, err := transport.BuildGrant(connPriv, transport.GrantParams{
		Delegate: delegatePub, Target: walletPubkey, HubXOnly: hubXOnly,
		Method: constants.NIP47MethodCashStatus, ExpiresAt: time.Now().Add(time.Hour).Unix(),
		SingleUse: true,
	})
	require.NoError(t, err)

	// serve runs one request for the same single-use grant in its own envelope
	// (fresh nonce + slice proof) and reports whether it was answered.
	serve := func(nonce string) bool {
		binding := PrivateItemBinding{HubXOnly: hubXOnly, Nonce: nonce, NotAfter: notAfter}
		item := buildGrantItem(t, "g", walletPubkey, constants.NIP47MethodCashStatus, `{"scope":"all"}`,
			delegatePriv, grant, hubXOnly, nonce, notAfter)
		_, served := nip47svc.ServePrivateItem(context.TODO(), svc.LNClient, item, binding)
		return served
	}

	require.True(t, serve(strings.Repeat("11", 32)), "first use of a single-use grant must be served")
	require.False(t, serve(strings.Repeat("22", 32)), "second use of a single-use grant must be silenced")
}

func mustHash(t *testing.T, params json.RawMessage) string {
	t.Helper()
	h, err := transport.CanonicalParamsHash(params)
	require.NoError(t, err)
	return h
}
