package nip47

import (
	"encoding/json"
	"fmt"
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
)

// U-14: a lost join reply is recovered with cash_lookup by connection_pubkey: the
// joiner gets the join's own encrypted_details; another signer is answered by
// deadline; a lookup without an item proof is UNAUTHORIZED; nothing is written.
func TestJoinLookup(t *testing.T) {
	member := nostr.GeneratePrivateKey()
	memberPub, _ := nostr.GetPublicKey(member)
	h := newJoinHub(t, memberPub)

	params := joinParams(t, member, *h.app.WalletPubkey, 20_000)
	res, served := h.serve(t, "j", params, member, strings.Repeat("0b", 32))
	require.True(t, served)
	require.Nil(t, res.Error, "%+v", res.Error)
	var joined struct {
		EncryptedDetails string `json:"encrypted_details"`
	}
	require.NoError(t, json.Unmarshal(res.Result, &joined))
	var req struct {
		ConnectionPubkey string `json:"connection_pubkey"`
	}
	require.NoError(t, json.Unmarshal([]byte(params), &req))

	var records int64
	require.NoError(t, h.ts.DB.Model(&db.CircleJoinRecord{}).Count(&records).Error)
	require.EqualValues(t, 1, records, "the join commits its record")

	deadline := time.Now().Add(time.Minute).Unix()
	done, code := lookupAs(t, h, req.ConnectionPubkey, member, deadline, constants.NIP47MethodCashLookup)
	require.Nil(t, code)
	require.Equal(t, nipcash.LookupDone, done.Status)
	var body struct {
		EncryptedDetails string `json:"encrypted_details"`
	}
	require.NoError(t, json.Unmarshal(done.Result, &body))
	assert.Equal(t, joined.EncryptedDetails, body.EncryptedDetails, "the original response, byte for byte")

	other, code := lookupAs(t, h, req.ConnectionPubkey, nostr.GeneratePrivateKey(), deadline, constants.NIP47MethodCashLookup)
	require.Nil(t, code)
	assert.Equal(t, nipcash.LookupPending, other.Status, "a record under another signer is no match")

	require.NoError(t, h.ts.DB.Model(&db.CircleJoinRecord{}).Count(&records).Error)
	assert.EqualValues(t, 1, records, "a lookup writes nothing")
}

func lookupAs(t *testing.T, h *joinHub, connectionPubkey, signer string, deadline int64, method string) (*nipcash.CashLookupResult, *string) {
	t.Helper()
	lookupParams := fmt.Sprintf(`{"connection_pubkey":%q,"deadline":%d}`, connectionPubkey, deadline)
	nonce := strings.Repeat("0c", 32)
	notAfter := time.Now().Add(time.Minute).Unix()
	item := buildItem(t, "l", *h.app.WalletPubkey, method, lookupParams, signer, h.connPriv, h.hubXOnly, nonce, notAfter)
	r, served := h.svc.ServePrivateItem(t.Context(), h.ts.LNClient, item,
		PrivateItemBinding{HubXOnly: h.hubXOnly, Nonce: nonce, NotAfter: notAfter})
	require.True(t, served)
	if r.Error != nil {
		return nil, &r.Error.Code
	}
	var out nipcash.CashLookupResult
	require.NoError(t, json.Unmarshal(r.Result, &out))
	return &out, nil
}

// NIP-CW §Lost Join Reply: once a join's wallet is deleted and its deadline
// plus 6 minutes has passed, the Hub may drop the record and a lookup answers
// expired. A record the sweep has not reached yet is answered the same way;
// while the wallet lives, the record is kept and answered done.
func TestJoinLookup_DroppableRecordAnswersExpired(t *testing.T) {
	member := nostr.GeneratePrivateKey()
	memberPub, _ := nostr.GetPublicKey(member)
	h := newJoinHub(t, memberPub)

	params := joinParams(t, member, *h.app.WalletPubkey, 20_000)
	res, served := h.serve(t, "j", params, member, strings.Repeat("0d", 32))
	require.True(t, served)
	require.Nil(t, res.Error, "%+v", res.Error)
	var req struct {
		ConnectionPubkey string `json:"connection_pubkey"`
	}
	require.NoError(t, json.Unmarshal([]byte(params), &req))

	old := time.Now().Add(-10 * time.Minute).Unix()
	require.NoError(t, h.ts.DB.Model(&db.CircleJoinRecord{}).Where("connection_pubkey = ?", req.ConnectionPubkey).
		Update("deadline", old).Error)
	live, code := lookupAs(t, h, req.ConnectionPubkey, member, old, constants.NIP47MethodCashLookup)
	require.Nil(t, code)
	assert.Equal(t, nipcash.LookupDone, live.Status, "the wallet still exists: the record is kept")

	var rec db.CircleJoinRecord
	require.NoError(t, h.ts.DB.Where("connection_pubkey = ?", req.ConnectionPubkey).First(&rec).Error)
	require.NoError(t, h.ts.DB.Model(&db.CircleJoinRecord{}).Where("id = ?", rec.ID).
		Update("wallet_app_id", rec.WalletAppID+100_000).Error, "the wallet is gone")
	gone, code := lookupAs(t, h, req.ConnectionPubkey, member, old, constants.NIP47MethodCashLookup)
	require.Nil(t, code)
	assert.Equal(t, nipcash.LookupExpired, gone.Status)
}

// NIP-CW §Lost Join Reply: a join lookup is authorized by the joiner's item
// proof; one without it is UNAUTHORIZED, never answered by deadline.
func TestJoinLookup_WithoutItemProofUnauthorized(t *testing.T) {
	member := nostr.GeneratePrivateKey()
	memberPub, _ := nostr.GetPublicKey(member)
	h := newJoinHub(t, memberPub)
	connection, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	lookupParams := fmt.Sprintf(`{"connection_pubkey":%q,"deadline":%d}`, connection, time.Now().Add(time.Minute).Unix())
	nonce := strings.Repeat("0d", 32)
	notAfter := time.Now().Add(time.Minute).Unix()
	item := buildItem(t, "l", *h.app.WalletPubkey, constants.NIP47MethodCashLookup, lookupParams, member, h.connPriv, h.hubXOnly, nonce, notAfter)
	var unsigned transport.Item
	item.Proof = unsigned.Proof
	r, served := h.svc.ServePrivateItem(t.Context(), h.ts.LNClient, item,
		PrivateItemBinding{HubXOnly: h.hubXOnly, Nonce: nonce, NotAfter: notAfter})
	require.True(t, served)
	require.NotNil(t, r.Error)
	assert.Equal(t, constants.ERROR_UNAUTHORIZED, r.Error.Code)

	signed, code := lookupAs(t, h, connection, member, time.Now().Add(time.Minute).Unix(), constants.NIP47MethodCashLookup)
	require.Nil(t, code, "the same lookup with the joiner's proof is answered")
	assert.Equal(t, nipcash.LookupPending, signed.Status)
}
