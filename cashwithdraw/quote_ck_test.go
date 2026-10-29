package cashwithdraw

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nipIC"
	"github.com/ohstr/nmilat/nipcash"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/cashwallet"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

// ckSlice is a connection_key slice: the IA's key, the connection key and the
// holder's key its attestation names.
type ckSlice struct {
	iaPriv, iaPub, connKey string
	holder                 holder
}

func attestation(t *testing.T, iaPriv, connKey, claimant string) string {
	t.Helper()
	exp := time.Now().Add(24 * time.Hour).Unix()
	ev := &nostr.Event{Kind: nipIC.KindAttestation, CreatedAt: nostr.Now(), Tags: nostr.Tags{
		{"d", connKey}, {"p", claimant}, {"platform", "discord"},
		{"evidence", tests.TestAttestationEvidence(connKey)}, {"expiration", strconv.FormatInt(exp, 10)},
	}}
	require.NoError(t, ev.Sign(iaPriv))
	b, err := json.Marshal(ev)
	require.NoError(t, err)
	return string(b)
}

func (w *world) mintCK(t *testing.T, amount uint64) (*db.App, ckSlice) {
	t.Helper()
	ia := newHolder()
	_, err := apps.NewIdentityAuthorityManager(w.svc.DB).Add(ia.pub, "test-ia", nil)
	require.NoError(t, err)
	ck := ckSlice{iaPriv: ia.priv, iaPub: ia.pub, connKey: tests.NewTestConnectionKey(), holder: newHolder()}
	res, err := cashwallet.Create(context.TODO(), w.deps(), cashwallet.Params{HubApp: w.hub, Recipients: []cashwallet.RecipientInput{
		{IdentityType: db.CashIdentityConnectionKey, IdentityValue: ck.connKey, IAPubkey: ck.iaPub, AmountMloki: amount},
	}})
	require.NoError(t, err)
	return res.WalletApp, ck
}

// U-23.6 / U-23.22: a connection_key slice is quoted on its attestation
// (VerifySliceAttestation, the shared helper): the quote's i carries the IA as
// its third element; a structural failure names no slice (NOT_FOUND); an IA no
// longer trusted is RESTRICTED without detail at the claimed-slice row, ahead
// of the maximum's QUOTA_EXCEEDED (XQ-29(7)).
func TestU_23_22_ConnectionKeyQuote(t *testing.T) {
	w := newWorld(t)
	bill, ck := w.mintCK(t, 21_000_000)
	params := func(claimant string) map[string]any {
		return map[string]any{"address": address(t, 0, 20), "identity_value": ck.connKey, "ia_pubkey": ck.iaPub,
			"attestation_event": attestation(t, ck.iaPriv, ck.connKey, claimant)}
	}
	res, err := w.quote(t, bill, ck.holder, params(ck.holder.pub))
	require.NoError(t, err)
	q, err := nipcash.ParseWithdrawQuote(res.Quote)
	require.NoError(t, err)
	assert.Equal(t, ck.connKey, q.IdentityValue)
	assert.Equal(t, ck.iaPub, q.IAPubkey)

	// The call, by the holder's connection-key credential.
	var ev nip01.Event
	require.NoError(t, json.Unmarshal([]byte(params(ck.holder.pub)["attestation_event"].(string)), &ev))
	att, err := nipIC.ParseAttestation(&ev)
	require.NoError(t, err)
	cred, err := nipcash.BySigningConnectionKey(ck.holder.priv, att)
	require.NoError(t, err)
	p, err := w.withdraw(t, bill, ck.holder, request(t, bill, cred, q, newRID(), nil))
	require.NoError(t, err)
	assert.Equal(t, nipcash.PayoutQueued, p.State)
	bill, ck = w.mintCK(t, 21_000_000)

	// The attestation names another holder: no slice.
	_, err = w.quote(t, bill, ck.holder, params(newHolder().pub))
	requireCode(t, err, constants.ERROR_NOT_FOUND, "")

	// Untrusted IA, on a slice also above the maximum: the claimed-slice row wins.
	big, ck2 := w.mintCK(t, 2_000_000_000)
	require.NoError(t, apps.NewIdentityAuthorityManager(w.svc.DB).Delete(ck2.iaPub))
	_, err = w.quote(t, big, ck2.holder, map[string]any{"address": address(t, 0, 20), "identity_value": ck2.connKey,
		"ia_pubkey": ck2.iaPub, "attestation_event": attestation(t, ck2.iaPriv, ck2.connKey, ck2.holder.pub)})
	requireCode(t, err, constants.ERROR_RESTRICTED, "")
}
