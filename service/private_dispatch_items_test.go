package service

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip44"
	"github.com/ohstr/nmilat/nipcash"
	"github.com/ohstr/nmilat/nipcash/transport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/nip47"
	"github.com/flokiorg/lokihub/tests"
)

// NIP-CASH §Batching Across Bills: a hub MUST evaluate each item on its own. An
// item this hub cannot serve, and one that is structurally malformed, both come
// before a real one here; the real one must still be answered.
func TestDispatchEnvelope_ServesItemsAfterOmittedAndMalformedOnes(t *testing.T) {
	ts, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer ts.Remove()
	relay := newScriptedRelay(t)
	require.NoError(t, ts.Cfg.SetUpdate("Relay", relay.url, ""))

	svc := &service{cfg: ts.Cfg, keys: ts.Keys,
		nip47Service: nip47.NewNip47Service(ts.DB, ts.Cfg, ts.Keys, ts.EventPublisher, nil)}
	pt, _ := newUnwrapFixture(t)

	hub := tests.CreateCashHub(t, ts, 100_000, 3600)
	tests.FundApp(ts, hub.ID, 10_000_000, "dispatchfund")
	realTarget, realPriv, realConnPriv := loopBill(t, ts, hub, "real")

	nonce, err := transport.NewNonce()
	require.NoError(t, err)
	replyTo, err := transport.NewNonce()
	require.NoError(t, err)
	notAfter := time.Now().Add(time.Minute).Unix()
	binding := nipcash.ItemBinding{HubXOnly: pt.nodeXOnly, Nonce: nonce, NotAfter: notAfter}

	ghost, err := nipcash.StatusItem("ghost", strings.Repeat("ee", 32), nostr.GeneratePrivateKey(),
		nipcash.CashStatusParams{}, nipcash.BySigning(nostr.GeneratePrivateKey()), binding)
	require.NoError(t, err)
	malformed := ghost
	malformed.ID = "malformed"
	malformed.BillProof = nil
	real, err := nipcash.StatusItem("real", realTarget, realConnPriv, nipcash.CashStatusParams{}, nipcash.BySigning(realPriv), binding)
	require.NoError(t, err)

	// Encode is strict, so the envelope is marshalled directly, the way a careless
	// or hostile assembler would send it.
	raw, err := json.Marshal(transport.Envelope{
		Version: transport.EnvelopeVersion, NotAfter: notAfter, Nonce: nonce, ReplyTo: replyTo,
		Items: []transport.Item{ghost, malformed, real},
	})
	require.NoError(t, err)
	limits := ts.Cfg.PrivateEnvelopeLimits()
	envelope, err := transport.Decode(raw, limits)
	require.NoError(t, err)
	require.Len(t, envelope.Rejected, 1, "Decode drops the malformed item")

	var conversationKey [32]byte
	_, err = rand.Read(conversationKey[:])
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	svc.dispatchEnvelope(ctx, nostr.NewSimplePool(ctx), pt, ts.LNClient, envelope, conversationKey)

	var reply *nostr.Event
	select {
	case reply = <-relay.published:
	case <-time.After(10 * time.Second):
		t.Fatal("no reply was published")
	}
	replyKey, err := transport.DeriveReplyKey(conversationKey, replyTo)
	require.NoError(t, err)
	plaintext, err := nip44.Decrypt(reply.Content, replyKey)
	require.NoError(t, err)
	ids := []string{"ghost", "malformed", "real"}
	decoded, err := transport.DecodeResponse([]byte(plaintext), nonce, ids, limits)
	require.NoError(t, err)

	require.Len(t, decoded.Results, 1)
	assert.Equal(t, "real", decoded.Results[0].ID, "the item after the bad ones is served")
	assert.Nil(t, decoded.Results[0].Error)
	assert.ElementsMatch(t, []string{"ghost", "malformed"}, decoded.Omitted(ids))
	assert.EqualValues(t, 2, pt.omittedItems.Load(), "both bad items are counted as omitted")
}
