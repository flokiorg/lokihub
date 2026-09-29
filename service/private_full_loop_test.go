package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	gonip44 "github.com/nbd-wtf/go-nostr/nip44"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/nip47"
	"github.com/flokiorg/lokihub/tests"
	"github.com/ohstr/nmilat/nipcash"
	"github.com/ohstr/nmilat/nipcash/transport"
)

// loopBill creates one funded, single-recipient bill and returns what a client needs to
// address it.
func loopBill(t *testing.T, svc *tests.TestService, hub *db.App, label string) (walletPubkey, recipientPriv string) {
	t.Helper()

	recipientPriv = nostr.GeneratePrivateKey()
	recipientPub, err := nostr.GetPublicKey(recipientPriv)
	require.NoError(t, err)

	wallet := db.App{
		Name: "loop-" + label, Kind: db.AppKindCashWallet,
		ParentAppID: &hub.ID, ParentKind: db.ParentKindCash,
		AppPubkey: tests.RandomHex32(),
	}
	require.NoError(t, svc.DB.Create(&wallet).Error)

	walletKey, err := svc.Keys.GetAppWalletKey(wallet.ID)
	require.NoError(t, err)
	walletPubkey, err = nostr.GetPublicKey(walletKey)
	require.NoError(t, err)
	require.NoError(t, svc.DB.Model(&wallet).Update("wallet_pubkey", walletPubkey).Error)

	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: recipientPub, AmountMloki: 1000},
	}))
	for _, scope := range []string{
		constants.CASH_REDEEM_SCOPE, constants.CASH_TRANSFER_SCOPE,
		constants.CASH_CONSOLIDATE_SCOPE, constants.GET_BALANCE_SCOPE,
	} {
		require.NoError(t, svc.DB.Create(&db.AppPermission{AppId: wallet.ID, Scope: scope}).Error)
	}
	return walletPubkey, recipientPriv
}

// TestFullLoop_SDKBuildsRealHubServesSDKReads is the contract test with no fake on either
// side.
//
// Everything below the relay hop is the genuine article: nmilat's own item constructors and
// envelope codec build the request, THIS hub's acceptsPrivateEvent, unwrap and
// ServePrivateItem serve it, this hub's own chunking and encryption produce the reply, and
// nmilat's DecodeResponse reads it back.
//
// The relay hop is deliberately not simulated here — nmilat's fake-hub test already covers
// publish, subscribe and reassembly. What this adds is the half that test could not: real
// bills in a real database, real claim rows, real permissions, real controllers.
//
// It is the test that would have caught F1 (the NIP-59 wire-format error) and the
// WrapRequest key bug on its own, since both were cases of two self-consistent halves
// disagreeing.
func TestFullLoop_SDKBuildsRealHubServesSDKReads(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	nip47svc := nip47.NewNip47Service(svc.DB, svc.Cfg, svc.Keys, svc.EventPublisher, nil)
	pt, _ := newUnwrapFixture(t)

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	tests.FundApp(svc, hub.ID, 10_000_000, "loopfund")

	// Three different bills, each with its own recipient key — the shape the transport
	// exists for, and the one nothing had exercised against a real database.
	const bills = 3
	targets := make([]string, 0, bills)
	privs := make([]string, 0, bills)
	for i := 0; i < bills; i++ {
		target, priv := loopBill(t, svc, hub, string(rune('a'+i)))
		targets = append(targets, target)
		privs = append(privs, priv)
	}

	// --- client side: build the envelope with the SDK's own constructors ---
	nonce, err := transport.NewNonce()
	require.NoError(t, err)
	replyTo, err := transport.NewNonce()
	require.NoError(t, err)
	notAfter := time.Now().Add(time.Minute).Unix()

	binding := nipcash.ItemBinding{HubXOnly: pt.nodeXOnly, Nonce: nonce, NotAfter: notAfter}
	envelope := transport.Envelope{
		Version: transport.EnvelopeVersion, NotAfter: notAfter, Nonce: nonce, ReplyTo: replyTo,
	}
	for i := 0; i < bills; i++ {
		item, err := nipcash.StatusItem("bill"+string(rune('a'+i)), targets[i],
			nipcash.CashStatusParams{}, nipcash.BySigning(privs[i]), binding)
		require.NoError(t, err)
		envelope.Items = append(envelope.Items, item)
	}

	limits := svc.Cfg.PrivateEnvelopeLimits()
	// The SDK's own coherence check, run before sending — it must pass against the same
	// hub identity the hub will verify against.
	require.NoError(t, envelope.Validate(pt.nodeXOnly, time.Now()),
		"the SDK built an envelope it considers incoherent")

	plaintext, err := envelope.Encode(limits)
	require.NoError(t, err)
	request, clientConversationKey, err := transport.WrapRequest(plaintext, pt.inboxXOnly)
	require.NoError(t, err)

	event, err := toGoNostrEvent(request)
	require.NoError(t, err)

	// --- hub side: this repo's real gates and dispatch ---
	require.True(t, pt.acceptsPrivateEvent(event), "this hub rejected an SDK-built request")

	got, hubConversationKey, err := pt.unwrap(event, limits, time.Now())
	require.NoError(t, err)
	require.Len(t, got.Items, bills)
	assert.Equal(t, clientConversationKey, hubConversationKey,
		"client and hub disagree on the conversation key; every reply would be unreadable")

	results := make([]transport.Result, 0, bills)
	for _, item := range got.Items {
		result, served := nip47svc.ServePrivateItem(context.TODO(), svc.LNClient, item,
			nip47.PrivateItemBinding{HubXOnly: pt.nodeXOnly, Nonce: got.Nonce, NotAfter: got.NotAfter})
		require.True(t, served, "item %s was omitted; it is a real bill with a real proof", item.ID)
		results = append(results, result)
	}

	// --- hub side: assemble and encrypt the reply exactly as publishPrivateReply does ---
	chunks, err := chunkResults(got.Nonce, results, limits)
	require.NoError(t, err)
	replyKey, err := transport.DeriveReplyKey(hubConversationKey, got.ReplyTo)
	require.NoError(t, err)

	// --- client side: read it back with the SDK's own decoder ---
	requestedIDs := make([]string, 0, bills)
	for _, item := range envelope.Items {
		requestedIDs = append(requestedIDs, item.ID)
	}

	clientReplyKey, err := transport.DeriveReplyKey(clientConversationKey, envelope.ReplyTo)
	require.NoError(t, err)

	answered := map[string]bool{}
	for _, chunk := range chunks {
		encoded, err := chunk.EncodeResponse(limits)
		require.NoError(t, err)
		sealed, err := gonip44.Encrypt(string(encoded), replyKey)
		require.NoError(t, err)

		opened, err := gonip44.Decrypt(sealed, clientReplyKey)
		require.NoError(t, err, "the client could not decrypt this hub's reply")

		decoded, err := transport.DecodeResponse([]byte(opened), envelope.Nonce, requestedIDs, limits)
		require.NoError(t, err, "the SDK could not decode this hub's reply")
		for _, r := range decoded.Results {
			require.NotNil(t, r.Result, "item %s came back with no roster", r.ID)
			answered[r.ID] = true
		}
	}

	for _, id := range requestedIDs {
		assert.True(t, answered[id], "item %s never came back", id)
	}
	assert.Len(t, answered, bills, "every bill must be answered exactly once")
}

// TestFullLoop_OmittedBillIsAbsentFromTheReply: an item this hub does not hold must simply
// not appear in the reply, and the SDK must still decode what does — the omission surviving
// all the way from the dispatch's decision to the client's decoder.
func TestFullLoop_OmittedBillIsAbsentFromTheReply(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	nip47svc := nip47.NewNip47Service(svc.DB, svc.Cfg, svc.Keys, svc.EventPublisher, nil)
	pt, _ := newUnwrapFixture(t)

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	tests.FundApp(svc, hub.ID, 10_000_000, "loopfund")
	realTarget, realPriv := loopBill(t, svc, hub, "real")

	nonce, err := transport.NewNonce()
	require.NoError(t, err)
	replyTo, err := transport.NewNonce()
	require.NoError(t, err)
	notAfter := time.Now().Add(time.Minute).Unix()
	binding := nipcash.ItemBinding{HubXOnly: pt.nodeXOnly, Nonce: nonce, NotAfter: notAfter}

	realItem, err := nipcash.StatusItem("real", realTarget, nipcash.CashStatusParams{}, nipcash.BySigning(realPriv), binding)
	require.NoError(t, err)
	// A bill this hub has never heard of, proven by a key it has never seen.
	ghostItem, err := nipcash.StatusItem("ghost", strings.Repeat("ee", 32),
		nipcash.CashStatusParams{}, nipcash.BySigning(nostr.GeneratePrivateKey()), binding)
	require.NoError(t, err)

	limits := svc.Cfg.PrivateEnvelopeLimits()
	envelope := transport.Envelope{
		Version: transport.EnvelopeVersion, NotAfter: notAfter, Nonce: nonce, ReplyTo: replyTo,
		Items: []transport.Item{realItem, ghostItem},
	}

	results := make([]transport.Result, 0, 2)
	for _, item := range envelope.Items {
		result, served := nip47svc.ServePrivateItem(context.TODO(), svc.LNClient, item,
			nip47.PrivateItemBinding{HubXOnly: pt.nodeXOnly, Nonce: nonce, NotAfter: notAfter})
		if !served {
			continue
		}
		results = append(results, result)
	}
	require.Len(t, results, 1, "exactly the real bill should be served")

	chunks, err := chunkResults(nonce, results, limits)
	require.NoError(t, err)
	require.Len(t, chunks, 1)

	decoded, err := transport.DecodeResponse(mustEncode(t, chunks[0], limits), nonce,
		[]string{"real", "ghost"}, limits)
	require.NoError(t, err, "a reply with an omission must still decode")

	assert.Len(t, decoded.Results, 1)
	assert.Equal(t, "real", decoded.Results[0].ID)
	// The SDK's own view of what went unanswered.
	assert.Equal(t, []string{"ghost"}, decoded.Omitted([]string{"real", "ghost"}),
		"the ghost bill must read as omitted, not as an error")
}

func mustEncode(t *testing.T, r transport.ResponseEnvelope, limits transport.Limits) []byte {
	t.Helper()
	b, err := r.EncodeResponse(limits)
	require.NoError(t, err)
	return b
}

// loopBillWithCoRecipient creates one funded bill split between TWO recipients, and
// returns what the first of them needs to address it plus both identities.
//
// The single-recipient loopBill above cannot exercise scoping at all: with one row,
// "mine" and "all" are the same answer, so a scoping bug would pass unnoticed.
func loopBillWithCoRecipient(t *testing.T, svc *tests.TestService, hub *db.App, label string) (walletPubkey, minePriv, minePub, theirsPub string) {
	t.Helper()

	minePriv = nostr.GeneratePrivateKey()
	minePub, err := nostr.GetPublicKey(minePriv)
	require.NoError(t, err)
	theirsPub, err = nostr.GetPublicKey(nostr.GeneratePrivateKey())
	require.NoError(t, err)

	wallet := db.App{
		Name: "loopscope-" + label, Kind: db.AppKindCashWallet,
		ParentAppID: &hub.ID, ParentKind: db.ParentKindCash,
		AppPubkey: tests.RandomHex32(),
	}
	require.NoError(t, svc.DB.Create(&wallet).Error)

	walletKey, err := svc.Keys.GetAppWalletKey(wallet.ID)
	require.NoError(t, err)
	walletPubkey, err = nostr.GetPublicKey(walletKey)
	require.NoError(t, err)
	require.NoError(t, svc.DB.Model(&wallet).Update("wallet_pubkey", walletPubkey).Error)

	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: minePub, AmountMloki: 1000},
		{IdentityType: db.CashIdentityPubkey, IdentityValue: theirsPub, AmountMloki: 2000},
	}))
	for _, scope := range []string{
		constants.CASH_REDEEM_SCOPE, constants.CASH_TRANSFER_SCOPE,
		constants.CASH_CONSOLIDATE_SCOPE, constants.GET_BALANCE_SCOPE,
	} {
		require.NoError(t, svc.DB.Create(&db.AppPermission{AppId: wallet.ID, Scope: scope}).Error)
	}
	return walletPubkey, minePriv, minePub, theirsPub
}

// TestFullLoop_ScopeIsHonouredPerItem proves cash_status scoping end to end, with no fake
// on either side — the SDK builds the items, this hub's real dispatch and real controller
// answer them, and the SDK decodes the reply.
//
// Both items name the SAME bill and differ only in scope, which is what makes this a test of
// scoping rather than of two unrelated requests: one envelope, one dispatch pass, two
// different answers. It also pins that scope is decided per ITEM, not per envelope.
//
// The controller-level tests cover the rules; this covers the wiring between them — that the
// proof's signer actually reaches the controller as the scope subject. That wiring is the
// part a unit test cannot see, and getting it wrong would either leak every co-recipient or
// return nothing at all, both while every controller test still passed.
func TestFullLoop_ScopeIsHonouredPerItem(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	nip47svc := nip47.NewNip47Service(svc.DB, svc.Cfg, svc.Keys, svc.EventPublisher, nil)
	pt, _ := newUnwrapFixture(t)

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	tests.FundApp(svc, hub.ID, 10_000_000, "scopefund")

	target, minePriv, minePub, theirsPub := loopBillWithCoRecipient(t, svc, hub, "a")

	nonce, err := transport.NewNonce()
	require.NoError(t, err)
	replyTo, err := transport.NewNonce()
	require.NoError(t, err)
	notAfter := time.Now().Add(time.Minute).Unix()

	binding := nipcash.ItemBinding{HubXOnly: pt.nodeXOnly, Nonce: nonce, NotAfter: notAfter}
	envelope := transport.Envelope{
		Version: transport.EnvelopeVersion, NotAfter: notAfter, Nonce: nonce, ReplyTo: replyTo,
	}

	// Absent scope: the private transport's default, which must be "mine".
	bare, err := nipcash.StatusItem("bare", target, nipcash.CashStatusParams{}, nipcash.BySigning(minePriv), binding)
	require.NoError(t, err)
	// Explicit "all": scoping is a default, not a removal.
	all, err := nipcash.StatusItem("all", target, nipcash.CashStatusParams{Scope: nipcash.ScopeAll}, nipcash.BySigning(minePriv), binding)
	require.NoError(t, err)
	envelope.Items = append(envelope.Items, bare, all)

	limits := svc.Cfg.PrivateEnvelopeLimits()
	require.NoError(t, envelope.Validate(pt.nodeXOnly, time.Now()))

	plaintext, err := envelope.Encode(limits)
	require.NoError(t, err)
	request, clientConversationKey, err := transport.WrapRequest(plaintext, pt.inboxXOnly)
	require.NoError(t, err)
	event, err := toGoNostrEvent(request)
	require.NoError(t, err)

	require.True(t, pt.acceptsPrivateEvent(event))
	got, hubConversationKey, err := pt.unwrap(event, limits, time.Now())
	require.NoError(t, err)
	require.Len(t, got.Items, 2)

	results := make([]transport.Result, 0, 2)
	for _, item := range got.Items {
		result, served := nip47svc.ServePrivateItem(context.TODO(), svc.LNClient, item,
			nip47.PrivateItemBinding{HubXOnly: pt.nodeXOnly, Nonce: got.Nonce, NotAfter: got.NotAfter})
		require.True(t, served, "item %s was omitted", item.ID)
		results = append(results, result)
	}

	chunks, err := chunkResults(got.Nonce, results, limits)
	require.NoError(t, err)
	replyKey, err := transport.DeriveReplyKey(hubConversationKey, got.ReplyTo)
	require.NoError(t, err)
	clientReplyKey, err := transport.DeriveReplyKey(clientConversationKey, envelope.ReplyTo)
	require.NoError(t, err)

	rosters := map[string]nipcash.CashStatusResult{}
	for _, chunk := range chunks {
		encoded, err := chunk.EncodeResponse(limits)
		require.NoError(t, err)
		sealed, err := gonip44.Encrypt(string(encoded), replyKey)
		require.NoError(t, err)
		opened, err := gonip44.Decrypt(sealed, clientReplyKey)
		require.NoError(t, err)
		decoded, err := transport.DecodeResponse([]byte(opened), envelope.Nonce, []string{"bare", "all"}, limits)
		require.NoError(t, err)
		for _, r := range decoded.Results {
			require.NotNil(t, r.Result, "item %s came back with no roster", r.ID)
			var roster nipcash.CashStatusResult
			require.NoError(t, json.Unmarshal(r.Result, &roster))
			rosters[r.ID] = roster
		}
	}
	require.Len(t, rosters, 2, "both items must be answered")

	// The default: only the caller's own row, and nothing about the co-recipient.
	require.Len(t, rosters["bare"].Recipients, 1,
		"an unscoped private read returned %d rows; the default must be the caller's own row alone",
		len(rosters["bare"].Recipients))
	assert.Equal(t, minePub, rosters["bare"].Recipients[0].IdentityValue)
	assert.Equal(t, uint64(1000), rosters["bare"].Recipients[0].AmountMillis)
	for _, r := range rosters["bare"].Recipients {
		assert.NotEqual(t, theirsPub, r.IdentityValue, "a co-recipient leaked through the real dispatch path")
	}

	// Explicitly asked for: the shared roster, both rows.
	assert.Len(t, rosters["all"].Recipients, 2, "scope=all must still return the full roster")
}
