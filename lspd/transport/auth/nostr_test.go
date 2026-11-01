package auth

import (
	"bytes"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// resetReplayCache isolates tests from each other's replay-cache state,
// since seenEvents is a package-level singleton.
func resetReplayCache(t *testing.T) {
	t.Helper()
	seenEventsMu.Lock()
	seenEvents = make(map[string]int64)
	seenEventsMu.Unlock()
}

type authEventOpts struct {
	method      string
	url         string
	createdAt   int64
	payloadHash string // empty = no payload tag
	kind        int
}

func buildAuthHeader(t *testing.T, sk string, opts authEventOpts) string {
	t.Helper()

	kind := opts.kind
	if kind == 0 {
		kind = KindHttpAuth
	}

	tags := nostr.Tags{
		{"u", opts.url},
		{"m", opts.method},
	}
	if opts.payloadHash != "" {
		tags = append(tags, nostr.Tag{"payload", opts.payloadHash})
	}

	event := nostr.Event{
		Kind:      kind,
		CreatedAt: nostr.Timestamp(opts.createdAt),
		Tags:      tags,
		Content:   "",
	}
	require.NoError(t, event.Sign(sk))

	eventJson, err := event.MarshalJSON()
	require.NoError(t, err)

	return "Nostr " + base64.StdEncoding.EncodeToString(eventJson)
}

func newTestKeypair(t *testing.T) (sk, pk string) {
	t.Helper()
	sk = nostr.GeneratePrivateKey()
	pk, err := nostr.GetPublicKey(sk)
	require.NoError(t, err)
	return sk, pk
}

func TestVerifyNostrAuth_ValidGetRequestSucceeds(t *testing.T) {
	resetReplayCache(t)
	sk, pk := newTestKeypair(t)

	r := httptest.NewRequest(http.MethodGet, "http://example.com/dashboard", nil)
	r.Header.Set("Authorization", buildAuthHeader(t, sk, authEventOpts{
		method:    http.MethodGet,
		url:       "http://example.com/dashboard",
		createdAt: time.Now().Unix(),
	}))

	session, err := VerifyNostrAuth(r, Options{})
	require.NoError(t, err)
	assert.Equal(t, pk, session.PubKey)
}

func TestVerifyNostrAuth_MissingAuthHeaderRejected(t *testing.T) {
	resetReplayCache(t)
	r := httptest.NewRequest(http.MethodGet, "http://example.com/dashboard", nil)

	_, err := VerifyNostrAuth(r, Options{})
	assert.Error(t, err)
}

func TestVerifyNostrAuth_WrongKindRejected(t *testing.T) {
	resetReplayCache(t)
	sk, _ := newTestKeypair(t)

	r := httptest.NewRequest(http.MethodGet, "http://example.com/dashboard", nil)
	r.Header.Set("Authorization", buildAuthHeader(t, sk, authEventOpts{
		method:    http.MethodGet,
		url:       "http://example.com/dashboard",
		createdAt: time.Now().Unix(),
		kind:      1, // not KindHttpAuth
	}))

	_, err := VerifyNostrAuth(r, Options{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid event kind")
}

func TestVerifyNostrAuth_TamperedSignatureRejected(t *testing.T) {
	resetReplayCache(t)
	sk, _ := newTestKeypair(t)

	header := buildAuthHeader(t, sk, authEventOpts{
		method:    http.MethodGet,
		url:       "http://example.com/dashboard",
		createdAt: time.Now().Unix(),
	})

	// Flip a character deep in the base64 payload to corrupt the signed event.
	tampered := header[:len(header)-4] + "AAAA"

	r := httptest.NewRequest(http.MethodGet, "http://example.com/dashboard", nil)
	r.Header.Set("Authorization", tampered)

	_, err := VerifyNostrAuth(r, Options{})
	assert.Error(t, err)
}

func TestVerifyNostrAuth_ExpiredTimestampRejected(t *testing.T) {
	resetReplayCache(t)
	sk, _ := newTestKeypair(t)

	r := httptest.NewRequest(http.MethodGet, "http://example.com/dashboard", nil)
	r.Header.Set("Authorization", buildAuthHeader(t, sk, authEventOpts{
		method:    http.MethodGet,
		url:       "http://example.com/dashboard",
		createdAt: time.Now().Add(-5 * time.Minute).Unix(),
	}))

	_, err := VerifyNostrAuth(r, Options{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "timestamp")
}

func TestVerifyNostrAuth_FutureTimestampRejected(t *testing.T) {
	resetReplayCache(t)
	sk, _ := newTestKeypair(t)

	r := httptest.NewRequest(http.MethodGet, "http://example.com/dashboard", nil)
	r.Header.Set("Authorization", buildAuthHeader(t, sk, authEventOpts{
		method:    http.MethodGet,
		url:       "http://example.com/dashboard",
		createdAt: time.Now().Add(5 * time.Minute).Unix(),
	}))

	_, err := VerifyNostrAuth(r, Options{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "timestamp")
}

func TestVerifyNostrAuth_UrlMismatchRejected(t *testing.T) {
	resetReplayCache(t)
	sk, _ := newTestKeypair(t)

	r := httptest.NewRequest(http.MethodGet, "http://example.com/dashboard", nil)
	r.Header.Set("Authorization", buildAuthHeader(t, sk, authEventOpts{
		method:    http.MethodGet,
		url:       "http://example.com/some-other-path",
		createdAt: time.Now().Unix(),
	}))

	_, err := VerifyNostrAuth(r, Options{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "'u' tag mismatch")
}

func TestVerifyNostrAuth_MethodMismatchRejected(t *testing.T) {
	resetReplayCache(t)
	sk, _ := newTestKeypair(t)

	r := httptest.NewRequest(http.MethodPost, "http://example.com/dashboard", strings.NewReader("{}"))
	r.Header.Set("Authorization", buildAuthHeader(t, sk, authEventOpts{
		method:      http.MethodGet, // signed for GET, request is POST
		url:         "http://example.com/dashboard",
		createdAt:   time.Now().Unix(),
		payloadHash: ComputePayloadHash([]byte("{}")),
	}))

	_, err := VerifyNostrAuth(r, Options{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "'m' tag mismatch")
}

// TestVerifyNostrAuth_MissingPayloadHashRejectedForPost is F-7: previously
// the payload hash check was entirely commented out, so a validly signed
// auth event for one POST body could be replayed against any other POST
// body to the same URL within the timestamp window.
func TestVerifyNostrAuth_MissingPayloadHashRejectedForPost(t *testing.T) {
	resetReplayCache(t)
	sk, _ := newTestKeypair(t)

	r := httptest.NewRequest(http.MethodPost, "http://example.com/dashboard", strings.NewReader(`{"method":"get_summary"}`))
	r.Header.Set("Authorization", buildAuthHeader(t, sk, authEventOpts{
		method:    http.MethodPost,
		url:       "http://example.com/dashboard",
		createdAt: time.Now().Unix(),
		// no payloadHash set
	}))

	_, err := VerifyNostrAuth(r, Options{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "payload")
}

func TestVerifyNostrAuth_WrongPayloadHashRejectedForPost(t *testing.T) {
	resetReplayCache(t)
	sk, _ := newTestKeypair(t)

	r := httptest.NewRequest(http.MethodPost, "http://example.com/dashboard", strings.NewReader(`{"method":"get_summary"}`))
	r.Header.Set("Authorization", buildAuthHeader(t, sk, authEventOpts{
		method:      http.MethodPost,
		url:         "http://example.com/dashboard",
		createdAt:   time.Now().Unix(),
		payloadHash: ComputePayloadHash([]byte(`{"method":"a completely different body"}`)),
	}))

	_, err := VerifyNostrAuth(r, Options{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "payload")
}

func TestVerifyNostrAuth_CorrectPayloadHashAcceptedAndBodyStillReadable(t *testing.T) {
	resetReplayCache(t)
	sk, pk := newTestKeypair(t)
	body := `{"method":"get_summary"}`

	r := httptest.NewRequest(http.MethodPost, "http://example.com/dashboard", strings.NewReader(body))
	r.Header.Set("Authorization", buildAuthHeader(t, sk, authEventOpts{
		method:      http.MethodPost,
		url:         "http://example.com/dashboard",
		createdAt:   time.Now().Unix(),
		payloadHash: ComputePayloadHash([]byte(body)),
	}))

	session, err := VerifyNostrAuth(r, Options{})
	require.NoError(t, err)
	assert.Equal(t, pk, session.PubKey)

	// Downstream JSON-RPC handling reads r.Body after auth — it must still
	// see the original bytes, since VerifyNostrAuth had to consume the
	// stream once already to hash it.
	remaining := make([]byte, len(body))
	n, _ := r.Body.Read(remaining)
	assert.Equal(t, body, string(remaining[:n]))
}

// TestVerifyNostrAuth_ReplayedEventRejectedOnSecondUse is the other half of
// F-7: even with a correct payload hash, a captured valid event must not be
// usable twice.
func TestVerifyNostrAuth_ReplayedEventRejectedOnSecondUse(t *testing.T) {
	resetReplayCache(t)
	sk, pk := newTestKeypair(t)

	header := buildAuthHeader(t, sk, authEventOpts{
		method:    http.MethodGet,
		url:       "http://example.com/dashboard",
		createdAt: time.Now().Unix(),
	})

	r1 := httptest.NewRequest(http.MethodGet, "http://example.com/dashboard", nil)
	r1.Header.Set("Authorization", header)
	session, err := VerifyNostrAuth(r1, Options{})
	require.NoError(t, err)
	assert.Equal(t, pk, session.PubKey)

	r2 := httptest.NewRequest(http.MethodGet, "http://example.com/dashboard", nil)
	r2.Header.Set("Authorization", header)
	_, err = VerifyNostrAuth(r2, Options{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "replay")
}

func TestVerifyNostrAuth_QueryParamFallback(t *testing.T) {
	resetReplayCache(t)
	sk, pk := newTestKeypair(t)

	header := buildAuthHeader(t, sk, authEventOpts{
		method:    http.MethodGet,
		url:       "http://example.com/dashboard",
		createdAt: time.Now().Unix(),
	})
	// strip the "Nostr " prefix for the query-param form, and
	// percent-encode it since raw base64 can contain '+' and '/', which
	// url.Query() would otherwise misinterpret.
	encoded := strings.TrimPrefix(header, "Nostr ")
	escaped := url.QueryEscape(encoded)

	r := httptest.NewRequest(http.MethodGet, "http://example.com/dashboard?auth="+escaped, nil)

	session, err := VerifyNostrAuth(r, Options{AllowQueryToken: true})
	require.NoError(t, err)
	assert.Equal(t, pk, session.PubKey)
}

func TestVerifyNostrAuth_QueryTokenRefusedUnlessAllowed(t *testing.T) {
	resetReplayCache(t)
	sk, _ := newTestKeypair(t)

	header := buildAuthHeader(t, sk, authEventOpts{
		method:    http.MethodGet,
		url:       "http://example.com/dashboard",
		createdAt: time.Now().Unix(),
	})
	escaped := url.QueryEscape(strings.TrimPrefix(header, "Nostr "))
	r := httptest.NewRequest(http.MethodGet, "http://example.com/dashboard?auth="+escaped, nil)

	_, err := VerifyNostrAuth(r, Options{})
	require.Error(t, err)
}

// countingReader counts the bytes pulled from a request body.
type countingReader struct {
	r io.Reader
	n int
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += n
	return n, err
}

func signedPost(t *testing.T, sk string, body []byte, rd io.Reader) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "http://example.com/dashboard", rd)
	r.Header.Set("Authorization", buildAuthHeader(t, sk, authEventOpts{
		method:      http.MethodPost,
		url:         "http://example.com/dashboard",
		createdAt:   time.Now().Unix(),
		payloadHash: ComputePayloadHash(body),
	}))
	return r
}

func TestVerifyNostrAuth_OtherSignerRefusedBeforeBodyIsRead(t *testing.T) {
	resetReplayCache(t)
	sk, _ := newTestKeypair(t)
	_, adminPK := newTestKeypair(t)
	body := bytes.Repeat([]byte("a"), 4<<20)
	cr := &countingReader{r: bytes.NewReader(body)}
	r := signedPost(t, sk, body, cr)

	_, err := VerifyNostrAuth(r, Options{PubKey: adminPK})
	require.ErrorIs(t, err, ErrForbidden)
	assert.Zero(t, cr.n, "bytes read from a refused signer's body")
}

func TestVerifyNostrAuth_AllowedSignerAccepted(t *testing.T) {
	resetReplayCache(t)
	sk, pk := newTestKeypair(t)
	body := []byte(`{"method":"get_summary"}`)
	r := signedPost(t, sk, body, bytes.NewReader(body))

	session, err := VerifyNostrAuth(r, Options{PubKey: pk})
	require.NoError(t, err)
	assert.Equal(t, pk, session.PubKey)
}

func TestVerifyNostrAuth_BodyCapBoundary(t *testing.T) {
	const limit = 1024
	sk, pk := newTestKeypair(t)

	t.Run("at the cap is accepted", func(t *testing.T) {
		resetReplayCache(t)
		body := bytes.Repeat([]byte("a"), limit)
		_, err := VerifyNostrAuth(signedPost(t, sk, body, bytes.NewReader(body)), Options{PubKey: pk, MaxBodyBytes: limit})
		require.NoError(t, err)
	})
	t.Run("one byte over is refused", func(t *testing.T) {
		resetReplayCache(t)
		body := bytes.Repeat([]byte("a"), limit+1)
		_, err := VerifyNostrAuth(signedPost(t, sk, body, bytes.NewReader(body)), Options{PubKey: pk, MaxBodyBytes: limit})
		require.ErrorIs(t, err, ErrBodyTooLarge)
	})
	t.Run("unknown length over the cap reads at most cap+1", func(t *testing.T) {
		resetReplayCache(t)
		body := bytes.Repeat([]byte("a"), 64*limit)
		cr := &countingReader{r: bytes.NewReader(body)}
		r := signedPost(t, sk, body, cr)
		_, err := VerifyNostrAuth(r, Options{PubKey: pk, MaxBodyBytes: limit})
		require.ErrorIs(t, err, ErrBodyTooLarge)
		assert.LessOrEqual(t, cr.n, limit+1, "bytes read past the cap")
	})
	t.Run("declared length over the cap is refused unread", func(t *testing.T) {
		resetReplayCache(t)
		body := bytes.Repeat([]byte("a"), limit+1)
		cr := &countingReader{r: bytes.NewReader(body)}
		r := signedPost(t, sk, body, cr)
		r.ContentLength = int64(len(body))
		_, err := VerifyNostrAuth(r, Options{PubKey: pk, MaxBodyBytes: limit})
		require.ErrorIs(t, err, ErrBodyTooLarge)
		assert.Zero(t, cr.n)
	})
}

// A captured token sent with a wrong body must not burn it for the real request.
func TestVerifyNostrAuth_FailedCheckDoesNotEnterReplayCache(t *testing.T) {
	resetReplayCache(t)
	sk, pk := newTestKeypair(t)
	body := []byte(`{"method":"get_summary"}`)
	header := buildAuthHeader(t, sk, authEventOpts{
		method:      http.MethodPost,
		url:         "http://example.com/dashboard",
		createdAt:   time.Now().Unix(),
		payloadHash: ComputePayloadHash(body),
	})

	bad := httptest.NewRequest(http.MethodPost, "http://example.com/dashboard", strings.NewReader(`{"method":"other"}`))
	bad.Header.Set("Authorization", header)
	_, err := VerifyNostrAuth(bad, Options{PubKey: pk})
	require.Error(t, err)

	good := httptest.NewRequest(http.MethodPost, "http://example.com/dashboard", bytes.NewReader(body))
	good.Header.Set("Authorization", header)
	_, err = VerifyNostrAuth(good, Options{PubKey: pk})
	require.NoError(t, err)

	again := httptest.NewRequest(http.MethodPost, "http://example.com/dashboard", bytes.NewReader(body))
	again.Header.Set("Authorization", header)
	_, err = VerifyNostrAuth(again, Options{PubKey: pk})
	require.ErrorContains(t, err, "replay")
}

func TestComputePayloadHash_DeterministicAndInputSensitive(t *testing.T) {
	h1 := ComputePayloadHash([]byte("hello"))
	h2 := ComputePayloadHash([]byte("hello"))
	h3 := ComputePayloadHash([]byte("world"))

	assert.Equal(t, h1, h2)
	assert.NotEqual(t, h1, h3)
}
