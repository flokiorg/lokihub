package nip47

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/ohstr/nmilat/nipcash"
	"github.com/ohstr/nmilat/nipcash/transport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/nip47/cipher"
	"github.com/flokiorg/lokihub/tests"
)

type lookupFixture struct {
	svc          *tests.TestService
	nip47svc     *nip47Service
	walletPubkey string
	connPriv     string
	walletID     uint
	ownerPriv    string
	ownerPub     string
	hubXOnly     string
}

func newLookupFixture(t *testing.T) *lookupFixture {
	t.Helper()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	t.Cleanup(svc.Remove)
	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	tests.FundApp(svc, hub.ID, 10_000_000, "fundtx")
	priv := nostr.GeneratePrivateKey()
	pub, _ := nostr.GetPublicKey(priv)
	walletPubkey, connPriv, walletID := auditBBill(t, svc, hub.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: pub, AmountMloki: 1000},
	})
	tests.FundApp(svc, walletID, 1000, "bill-fund")
	return &lookupFixture{
		svc: svc, nip47svc: NewNip47Service(svc.DB, svc.Cfg, svc.Keys, svc.EventPublisher, nil),
		walletPubkey: walletPubkey, connPriv: connPriv, walletID: walletID,
		ownerPriv: priv, ownerPub: pub, hubXOnly: strings.Repeat("ab", 32),
	}
}

func (f *lookupFixture) serve(t *testing.T, build func(b nipcash.ItemBinding) (transport.Item, error)) (transport.Result, bool) {
	t.Helper()
	nonce := tests.RandomHex32()
	notAfter := time.Now().Add(time.Minute).Unix()
	item, err := build(nipcash.ItemBinding{HubXOnly: f.hubXOnly, Nonce: nonce, NotAfter: notAfter})
	require.NoError(t, err)
	return f.nip47svc.ServePrivateItem(context.TODO(), f.svc.LNClient, item,
		PrivateItemBinding{HubXOnly: f.hubXOnly, Nonce: nonce, NotAfter: notAfter, AuthFailures: new(int)})
}

func (f *lookupFixture) lookup(t *testing.T, cred nipcash.Credential, req nipcash.CashLookupRequest) (transport.Result, bool) {
	return f.serve(t, func(b nipcash.ItemBinding) (transport.Item, error) {
		return nipcash.CashLookupParams{Credential: cred, Request: req}.Item("l", f.walletPubkey, f.connPriv, b)
	})
}

func decryptToken(t *testing.T, walletPubkey, recipientPriv, enc string) string {
	t.Helper()
	c, err := cipher.NewNip47Cipher(constants.ENCRYPTION_TYPE_NIP44_V2, walletPubkey, recipientPriv)
	require.NoError(t, err)
	plain, err := c.Decrypt(enc)
	require.NoError(t, err)
	return plain
}

// U-10: a split's lost reply is rebuilt by cash_lookup: done, the same fields, the
// same tokens once decrypted, while its source bill is already destroyed. The
// lookup writes nothing.
func TestCashLookup_DoneRebuildsASplit(t *testing.T) {
	f := newLookupFixture(t)
	newPub, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	rid := tests.RandomHex32()
	split := uint64(400)

	res, served := f.serve(t, func(b nipcash.ItemBinding) (transport.Item, error) {
		return nipcash.CashTransferParams{
			Credential: nipcash.BySigning(f.ownerPriv), To: nipcash.Pubkey(newPub),
			CurrentAmount: 1000, SplitAmount: &split, RequestID: rid,
		}.Item("t", f.walletPubkey, f.connPriv, b)
	})
	require.True(t, served)
	require.Nil(t, res.Error, "%+v", res.Error)
	var original map[string]any
	require.NoError(t, json.Unmarshal(res.Result, &original))

	var events, marks int64
	require.NoError(t, f.svc.DB.Model(&db.RequestEvent{}).Count(&events).Error)
	require.NoError(t, f.svc.DB.Model(&db.CashUsedRequestID{}).Count(&marks).Error)

	look, served := f.lookup(t, nipcash.BySigning(f.ownerPriv), nipcash.CashLookupRequest{RequestID: rid, Deadline: time.Now().Unix()})
	require.True(t, served)
	require.Nil(t, look.Error, "%+v", look.Error)
	var answer nipcash.CashLookupResult
	require.NoError(t, json.Unmarshal(look.Result, &answer))
	require.Equal(t, nipcash.LookupDone, answer.Status)
	var rebuilt map[string]any
	require.NoError(t, json.Unmarshal(answer.Result, &rebuilt))

	require.Equal(t, len(original), len(rebuilt), "the same field set")
	for _, k := range []string{"amount_millis", "identity_type", "identity_value", "remaining_amount_millis", "new_wallet_pubkey", "remainder_wallet_pubkey"} {
		assert.Equal(t, original[k], rebuilt[k], k)
	}
	for _, pair := range [][2]string{{"new_wallet_pubkey", "new_wallet_token"}, {"remainder_wallet_pubkey", "remainder_wallet_token"}} {
		w := original[pair[0]].(string)
		assert.Equal(t,
			decryptToken(t, w, f.ownerPriv, original[pair[1]].(string)),
			decryptToken(t, w, f.ownerPriv, rebuilt[pair[1]].(string)), "the same token, byte for byte")
	}

	var events2, marks2 int64
	require.NoError(t, f.svc.DB.Model(&db.RequestEvent{}).Count(&events2).Error)
	require.NoError(t, f.svc.DB.Model(&db.CashUsedRequestID{}).Count(&marks2).Error)
	assert.Equal(t, events, events2, "a lookup records no request event")
	assert.Equal(t, marks, marks2, "a lookup marks nothing")

	// Another signer is refused.
	stranger, served := f.lookup(t, nipcash.BySigning(nostr.GeneratePrivateKey()), nipcash.CashLookupRequest{RequestID: rid, Deadline: time.Now().Unix()})
	require.True(t, served)
	require.NotNil(t, stranger.Error)
	assert.Equal(t, constants.ERROR_UNAUTHORIZED, stranger.Error.Code)
}

// With no record the answer is by deadline: pending, then unknown, then expired.
func TestCashLookup_NoRecordAnswersByDeadline(t *testing.T) {
	f := newLookupFixture(t)
	now := time.Now().Unix()
	for _, c := range []struct {
		deadline int64
		want     nipcash.LookupStatus
	}{
		{now + 30, nipcash.LookupPending},
		{now - 30, nipcash.LookupUnknown},
		{now - 3600, nipcash.LookupExpired},
	} {
		look, served := f.lookup(t, nipcash.BySigning(f.ownerPriv), nipcash.CashLookupRequest{RequestID: tests.RandomHex32(), Deadline: c.deadline})
		require.True(t, served)
		require.Nil(t, look.Error)
		var answer nipcash.CashLookupResult
		require.NoError(t, json.Unmarshal(look.Result, &answer))
		assert.Equal(t, c.want, answer.Status)
		if c.want == nipcash.LookupPending {
			assert.Equal(t, c.deadline+1, answer.RetryAfter)
		}
	}
}

// A lookup naming both or neither of request_id and connection_pubkey is
// BAD_REQUEST; a target the Hub never held is omitted.
func TestCashLookup_ParamsAndTargets(t *testing.T) {
	f := newLookupFixture(t)
	look, served := f.serve(t, func(b nipcash.ItemBinding) (transport.Item, error) {
		return nipcash.BuildItem("l", f.walletPubkey, f.connPriv, constants.NIP47MethodCashLookup,
			map[string]any{"deadline": time.Now().Unix()}, nipcash.BySigning(f.ownerPriv), b)
	})
	require.True(t, served)
	require.NotNil(t, look.Error)
	assert.Equal(t, constants.ERROR_BAD_REQUEST, look.Error.Code)

	_, served = f.serve(t, func(b nipcash.ItemBinding) (transport.Item, error) {
		return nipcash.CashLookupParams{Credential: nipcash.BySigning(f.ownerPriv),
			Request: nipcash.CashLookupRequest{RequestID: tests.RandomHex32(), Deadline: time.Now().Unix()}}.
			Item("l", strings.Repeat("ef", 32), f.connPriv, b)
	})
	assert.False(t, served)
}
