package notifications

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	btcec "github.com/flokiorg/go-flokicoin/crypto"
	"github.com/flokiorg/lokihub/lspd/lightning"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testPrivKey(t *testing.T) *btcec.PrivateKey {
	t.Helper()
	priv, _ := btcec.PrivKeyFromBytes([]byte("0123456789abcdef0123456789abcde"))
	return priv
}

// loopbackTransport returns a transport allowed to reach httptest servers,
// which listen on 127.0.0.1 -- as with LSPS5_ALLOW_LOOPBACK_WEBHOOKS.
func loopbackTransport(priv interface{}) *HTTPSTransport {
	tr := NewHTTPSTransport(priv)
	tr.client = newWebhookClient(true)
	return tr
}

func TestHTTPSTransport_Send_SignsAndPosts(t *testing.T) {
	priv := testPrivKey(t)

	var gotBody []byte
	var gotTimestamp, gotSignature string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		gotTimestamp = r.Header.Get("x-lsps5-timestamp")
		gotSignature = r.Header.Get("x-lsps5-signature")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	transport := loopbackTransport(priv)
	err := transport.Send(context.Background(), server.URL, "lsps5.payment_incoming", map[string]string{"payment_hash": "abc"})
	require.NoError(t, err)

	assert.Contains(t, string(gotBody), "lsps5.payment_incoming")
	assert.NotEmpty(t, gotTimestamp)
	assert.NotEmpty(t, gotSignature)

	// The signature must actually verify against the timestamp+body it was
	// sent with, not just be present.
	pubkey, err := lightning.VerifyMessage(
		[]byte("LSPS5: DO NOT SIGN THIS MESSAGE MANUALLY: LSP: At "+gotTimestamp+" I notify "+string(gotBody)),
		gotSignature,
	)
	require.NoError(t, err)
	assert.True(t, priv.PubKey().IsEqual(pubkey))
}

func TestHTTPSTransport_Send_NonOKStatusIsError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	transport := loopbackTransport(testPrivKey(t))
	err := transport.Send(context.Background(), server.URL, "lsps5.payment_incoming", map[string]string{})
	assert.ErrorIs(t, err, ErrWebhookFailed)
}

// The webhook host is re-resolved at delivery, so the address policy must
// hold at connect time, not only at registration: by default a webhook that
// resolves to loopback is never contacted.
func TestHTTPSTransport_Send_RefusesLoopbackByDefault(t *testing.T) {
	hit := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	transport := NewHTTPSTransport(testPrivKey(t))
	err := transport.Send(context.Background(), server.URL, "lsps5.payment_incoming", map[string]string{})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrWebhookAddressNotAllowed)
	assert.False(t, hit, "the loopback server must never be contacted")
}

func TestWebhookDialControl(t *testing.T) {
	tests := []struct {
		address       string
		allowLoopback bool
		allowed       bool
	}{
		{"8.8.8.8:443", false, true},
		{"[2001:4860:4860::8888]:443", false, true},
		{"127.0.0.1:443", false, false},
		{"[::1]:443", false, false},
		{"0.0.0.0:443", false, false},
		{"10.0.0.1:443", false, false},
		{"172.16.0.1:443", false, false},
		{"192.168.1.1:443", false, false},
		{"169.254.169.254:80", false, false},
		{"[fd00::1]:443", false, false},
		{"127.0.0.1:443", true, true},
		{"[::1]:443", true, true},
		{"10.0.0.1:443", true, false},
		{"169.254.169.254:80", true, false},
		{"not-an-ip:443", false, false},
	}
	for _, tt := range tests {
		err := webhookDialControl(tt.allowLoopback)("tcp", tt.address, nil)
		if tt.allowed {
			assert.NoError(t, err, "%s (allowLoopback=%v)", tt.address, tt.allowLoopback)
		} else {
			assert.ErrorIs(t, err, ErrWebhookAddressNotAllowed, "%s (allowLoopback=%v)", tt.address, tt.allowLoopback)
		}
	}
}

// A redirect must not be followed: it would let a webhook that passed
// validation hand delivery to an arbitrary second URL. The 3xx itself is a
// failed delivery.
func TestHTTPSTransport_Send_DoesNotFollowRedirects(t *testing.T) {
	targetHit := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetHit = true
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()

	transport := loopbackTransport(testPrivKey(t))
	err := transport.Send(context.Background(), redirector.URL, "lsps5.payment_incoming", map[string]string{})
	assert.ErrorIs(t, err, ErrWebhookFailed)
	assert.False(t, targetHit, "the redirect target must never be contacted")
}

func TestNotificationService_SetAllowLoopbackWebhooks(t *testing.T) {
	s := NewNotificationService(nil, nil, nil)
	tr := s.transports[TransportTypePush].(*HTTPSTransport)
	tr.nodePrivKey = testPrivKey(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	assert.ErrorIs(t, tr.Send(context.Background(), server.URL, "lsps5.payment_incoming", map[string]string{}), ErrWebhookAddressNotAllowed)
	s.SetAllowLoopbackWebhooks(true)
	assert.NoError(t, tr.Send(context.Background(), server.URL, "lsps5.payment_incoming", map[string]string{}))
}

func TestHTTPSTransport_Send_InvalidPrivateKeyType(t *testing.T) {
	transport := NewHTTPSTransport("not a private key")
	err := transport.Send(context.Background(), "http://example.invalid", "lsps5.payment_incoming", map[string]string{})
	assert.ErrorIs(t, err, ErrInvalidPrivateKey)
}

func TestHTTPSTransport_Type(t *testing.T) {
	transport := NewHTTPSTransport(testPrivKey(t))
	assert.Equal(t, TransportTypePush, transport.Type())
}
