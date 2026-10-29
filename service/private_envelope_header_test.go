package service

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip44"
	"github.com/ohstr/nmilat/nipcash/transport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sealPlaintext seals a raw envelope plaintext to pt's inbox, as a client would.
func sealPlaintext(t *testing.T, pt *privateTransport, plain string) *nostr.Event {
	t.Helper()
	priv := nostr.GeneratePrivateKey()
	pub, _ := nostr.GetPublicKey(priv)
	ck, err := nip44.GenerateConversationKey(pt.inboxXOnly, priv)
	require.NoError(t, err)
	ct, err := nip44.Encrypt(plain, ck)
	require.NoError(t, err)
	return &nostr.Event{Kind: transport.KindPrivateRequest, PubKey: pub, Tags: nostr.Tags{{"p", pt.inboxXOnly}}, Content: ct}
}

func envelopeHead(nonce string, notAfter int64) string {
	reply, _ := transport.NewNonce()
	item := `{"id":"1","target":"` + strings.Repeat("cc", 32) + `","method":"cash_status","params":{},"proof":{"kind":23192},"bill_proof":{"kind":23193}}`
	return `{"nonce":"` + nonce + `","reply_to":"` + reply + `","not_after":` + strconv.FormatInt(notAfter, 10) + `,"items":[` + item + `]`
}

// NIP-CASH §Envelopes (E3, E4): a served nonce never runs again and a not_after
// more than max_request_age ahead never runs, whatever key spelling carries them.
func TestUnwrap_CaseVariantHeaderKeysAreDropped(t *testing.T) {
	pt, seal := newUnwrapFixture(t)
	limits := transport.DefaultLimits()
	now := time.Now()
	served := testEnvelope(t)
	_, _, err := pt.unwrap(seal(t, served), limits, now)
	require.NoError(t, err)

	fresh, _ := transport.NewNonce()
	_, _, err = pt.unwrap(sealPlaintext(t, pt, envelopeHead(fresh, now.Unix()+60)+`,"Nonce":"`+served.Nonce+`"}`), limits, now)
	require.Error(t, err, "a served nonce behind a case-variant key")
	assert.False(t, errors.As(err, new(*envelopeRefusal)), "dropped, not answered")

	other, _ := transport.NewNonce()
	far := now.Unix() + 1000*limits.MaxRequestAgeSeconds
	got, _, err := pt.unwrap(sealPlaintext(t, pt, envelopeHead(other, now.Unix()+60)+`,"NOT_AFTER":`+strconv.FormatInt(far, 10)+`}`), limits, now)
	require.Error(t, err, "a far not_after behind a case-variant key ran with %+v", got)
	assert.False(t, pt.nonces.Seen(other), "a dropped envelope's nonce is not marked")

	third, _ := transport.NewNonce()
	_, _, err = pt.unwrap(sealPlaintext(t, pt, envelopeHead(third, now.Unix()+60)+`,"items_extra":1}`), limits, now)
	assert.NoError(t, err, "an unrelated extra key is no case variant")
}

func TestUnwrap_NotAfterAtMaxRequestAgeRunsAndOneSecondPastIsRefused(t *testing.T) {
	pt, _ := newUnwrapFixture(t)
	limits := transport.DefaultLimits()
	now := time.Now()
	atBound, _ := transport.NewNonce()
	env, _, err := pt.unwrap(sealPlaintext(t, pt, envelopeHead(atBound, now.Unix()+limits.MaxRequestAgeSeconds)+`}`), limits, now)
	require.NoError(t, err)
	assert.Equal(t, now.Unix()+limits.MaxRequestAgeSeconds, env.NotAfter)

	past, _ := transport.NewNonce()
	_, _, err = pt.unwrap(sealPlaintext(t, pt, envelopeHead(past, now.Unix()+limits.MaxRequestAgeSeconds+1)+`}`), limits, now)
	var refusal *envelopeRefusal
	require.True(t, errors.As(err, &refusal), "E4 refuses with BAD_REQUEST: %v", err)
	assert.Equal(t, "BAD_REQUEST", refusal.code)
}

// NIP-CASH §Serving an Envelope: once an envelope passes E4 its nonce counts as
// served, so a copy of one refused at E5 is dropped, not answered again; one
// refused at E4 is not served and is refused again.
func TestUnwrap_E5RefusalMarksTheNonceServed(t *testing.T) {
	pt, _ := newUnwrapFixture(t)
	limits := transport.DefaultLimits()
	now := time.Now()
	nonce, _ := transport.NewNonce()
	reply, _ := transport.NewNonce()
	items := make([]string, 0, limits.MaxItems+1)
	for i := 0; i <= limits.MaxItems; i++ {
		items = append(items, fmt.Sprintf(`{"id":"%d","target":"%s","method":"cash_status","bill_proof":{"kind":23193}}`, i, strings.Repeat("cc", 32)))
	}
	ev := sealPlaintext(t, pt, fmt.Sprintf(`{"nonce":"%s","reply_to":"%s","not_after":%d,"items":[%s]}`, nonce, reply, now.Unix()+60, strings.Join(items, ",")))

	_, _, err := pt.unwrap(ev, limits, now)
	var refusal *envelopeRefusal
	require.True(t, errors.As(err, &refusal), "first copy: %v", err)
	require.Equal(t, "QUOTA_EXCEEDED", refusal.code)
	_, _, err = pt.unwrap(ev, limits, now)
	require.Error(t, err)
	require.False(t, errors.As(err, &refusal), "a copy of an envelope refused at E5 was answered again")

	e4, _ := transport.NewNonce()
	bad := sealPlaintext(t, pt, envelopeHead(e4, now.Unix()+limits.MaxRequestAgeSeconds+1)+`}`)
	for i := 0; i < 2; i++ {
		_, _, err = pt.unwrap(bad, limits, now)
		require.True(t, errors.As(err, &refusal), "copy %d of an E4 refusal is refused again: %v", i, err)
	}
	assert.False(t, pt.nonces.Seen(e4))
}
