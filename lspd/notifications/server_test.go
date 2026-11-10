package notifications

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	ecies "github.com/flokiorg/lokihub/internal/eciesgo"
	btcec "github.com/flokiorg/go-flokicoin/crypto"
	"github.com/flokiorg/go-flokicoin/crypto/ecdsa"
	pb "github.com/flokiorg/lokihub/lspd/api/proto/notifications"
	rpc "github.com/flokiorg/lokihub/lspd/api/proto/rpc"
	"github.com/flokiorg/lokihub/lspd/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tv42/zbase32"
	"google.golang.org/protobuf/proto"
)

// signMessageForTest replicates lightning.VerifyMessage's exact signing
// scheme (SignedMsgPrefix || message, double-SHA256, compact ECDSA sign,
// zbase32-encode) so tests can construct a signature VerifyMessage will
// actually accept -- there's no exported general-purpose signer counterpart
// to VerifyMessage in the lightning package (SignLsps5Message wraps its
// own, different bLIP-55-specific message format, not a raw message).
func signMessageForTest(t *testing.T, privKey *btcec.PrivateKey, message []byte) string {
	t.Helper()
	msg := append([]byte("Lightning Signed Message:"), message...)
	first := sha256.Sum256(msg)
	second := sha256.Sum256(first[:])
	sig := ecdsa.SignCompact(privKey, second[:], true)
	return zbase32.EncodeToString(sig)
}

// serverTestFixture bundles a *server wired to a fakeNotifStore, the node's
// ECIES keypair (so tests can encrypt requests the way a real client
// would), and a ready-to-use context carrying that node -- everything
// SubscribeNotifications/UnsubscribeNotifications need via rpc.GetNode.
type serverTestFixture struct {
	srv     *server
	store   *fakeNotifStore
	nodeKey *ecies.PrivateKey
}

func newServerTestFixture(t *testing.T) *serverTestFixture {
	t.Helper()
	store := newFakeNotifStore()
	nodeKey, err := ecies.GenerateKey()
	require.NoError(t, err)

	srv, ok := NewNotificationsServer(store).(*server)
	require.True(t, ok)

	return &serverTestFixture{
		srv:     srv,
		store:   store,
		nodeKey: nodeKey,
	}
}

// buildEncryptedRequest builds a real, validly-signed-and-encrypted
// EncryptedNotificationRequest the way an actual LSPS5 client would, using
// clientPrivKeyHex to sign url and f.nodeKey's public half to encrypt --
// exercising SubscribeNotifications' full decrypt -> unmarshal -> verify
// pipeline rather than a hand-assembled shortcut.
func (f *serverTestFixture) buildEncryptedRequest(t *testing.T, clientPrivKey *btcec.PrivateKey, url, transport string, unsubscribe bool) []byte {
	t.Helper()
	sig := signMessageForTest(t, clientPrivKey, []byte(url))

	var payload []byte
	var err error
	if unsubscribe {
		payload, err = proto.Marshal(&pb.UnsubscribeNotificationsRequest{Url: url, Signature: sig})
	} else {
		payload, err = proto.Marshal(&pb.SubscribeNotificationsRequest{Url: url, Signature: sig, Transport: transport})
	}
	require.NoError(t, err)

	blob, err := ecies.Encrypt(f.nodeKey.PublicKey, payload)
	require.NoError(t, err)
	return blob
}

// TestSubscribeNotifications_ValidRequest_RegistersInStore is the happy
// path for SubscribeNotifications, previously entirely untested: decrypts
// with the node's ECIES key, unmarshals, verifies the client's signature
// over the URL, and registers pubkey/url/transport in the store.
func TestSubscribeNotifications_ValidRequest_RegistersInStore(t *testing.T) {
	f := newServerTestFixture(t)
	clientPrivKey, err := btcec.NewPrivateKey()
	require.NoError(t, err)

	blob := f.buildEncryptedRequest(t, clientPrivKey, "https://client.example/webhook", "push", false)
	ctx := rpc.WithNode(t.Context(), &core.Node{EciesPrivateKey: f.nodeKey}, "token")

	resp, err := f.srv.SubscribeNotifications(ctx, &pb.EncryptedNotificationRequest{Blob: blob})
	require.NoError(t, err)
	require.NotNil(t, resp)

	require.Len(t, f.store.registerCalls, 1)
	call := f.store.registerCalls[0]
	assert.Equal(t, "https://client.example/webhook", call.url)
	assert.Equal(t, "push", call.transport)

	wantPubkey := clientPrivKey.PubKey().SerializeCompressed()
	assert.Equal(t, wantPubkey, mustDecodeHexT(t, call.pubkey), "must register under the pubkey recovered from the signature, not an unrelated one")
}

// TestSubscribeNotifications_DefaultsTransportToPush covers the
// transport==""-defaults-to-"push" branch.
func TestSubscribeNotifications_DefaultsTransportToPush(t *testing.T) {
	f := newServerTestFixture(t)
	clientPrivKey, err := btcec.NewPrivateKey()
	require.NoError(t, err)

	blob := f.buildEncryptedRequest(t, clientPrivKey, "https://client.example/webhook", "", false)
	ctx := rpc.WithNode(t.Context(), &core.Node{EciesPrivateKey: f.nodeKey}, "token")

	_, err = f.srv.SubscribeNotifications(ctx, &pb.EncryptedNotificationRequest{Blob: blob})
	require.NoError(t, err)

	require.Len(t, f.store.registerCalls, 1)
	assert.Equal(t, "push", f.store.registerCalls[0].transport)
}

// TestSubscribeNotifications_NoNodeInContext covers the auth-context
// guard: rpc.GetNode fails when the gRPC middleware never attached a node
// (unauthenticated request).
func TestSubscribeNotifications_NoNodeInContext(t *testing.T) {
	f := newServerTestFixture(t)

	_, err := f.srv.SubscribeNotifications(t.Context(), &pb.EncryptedNotificationRequest{Blob: []byte("irrelevant")})
	assert.Error(t, err)
	assert.Empty(t, f.store.registerCalls)
}

// TestSubscribeNotifications_WrongEciesKeyFailsDecrypt covers the case
// where the blob was encrypted for a different node's key entirely --
// must fail cleanly, not panic or silently register garbage.
func TestSubscribeNotifications_WrongEciesKeyFailsDecrypt(t *testing.T) {
	f := newServerTestFixture(t)
	clientPrivKey, err := btcec.NewPrivateKey()
	require.NoError(t, err)

	blob := f.buildEncryptedRequest(t, clientPrivKey, "https://client.example/webhook", "push", false)

	wrongNodeKey, err := ecies.GenerateKey()
	require.NoError(t, err)
	ctx := rpc.WithNode(t.Context(), &core.Node{EciesPrivateKey: wrongNodeKey}, "token")

	_, err = f.srv.SubscribeNotifications(ctx, &pb.EncryptedNotificationRequest{Blob: blob})
	assert.Error(t, err)
	assert.Empty(t, f.store.registerCalls)
}

// TestSubscribeNotifications_StoreErrorReturnsInternalError covers the
// "decrypted and verified fine, but persistence failed" branch -- must
// surface ErrInternal, not leak the underlying store error.
func TestSubscribeNotifications_StoreErrorReturnsInternalError(t *testing.T) {
	f := newServerTestFixture(t)
	f.store.registerErr = assert.AnError
	clientPrivKey, err := btcec.NewPrivateKey()
	require.NoError(t, err)

	blob := f.buildEncryptedRequest(t, clientPrivKey, "https://client.example/webhook", "push", false)
	ctx := rpc.WithNode(t.Context(), &core.Node{EciesPrivateKey: f.nodeKey}, "token")

	_, err = f.srv.SubscribeNotifications(ctx, &pb.EncryptedNotificationRequest{Blob: blob})
	assert.ErrorIs(t, err, ErrInternal)
}

// TestUnsubscribeNotifications_ValidRequest_RemovesFromStore is the happy
// path for UnsubscribeNotifications, previously entirely untested.
func TestUnsubscribeNotifications_ValidRequest_RemovesFromStore(t *testing.T) {
	f := newServerTestFixture(t)
	clientPrivKey, err := btcec.NewPrivateKey()
	require.NoError(t, err)

	blob := f.buildEncryptedRequest(t, clientPrivKey, "https://client.example/webhook", "", true)
	ctx := rpc.WithNode(t.Context(), &core.Node{EciesPrivateKey: f.nodeKey}, "token")

	resp, err := f.srv.UnsubscribeNotifications(ctx, &pb.EncryptedNotificationRequest{Blob: blob})
	require.NoError(t, err)
	require.NotNil(t, resp)

	require.Len(t, f.store.unsubscribeCalls, 1)
	call := f.store.unsubscribeCalls[0]
	assert.Equal(t, "https://client.example/webhook", call.url)
	wantPubkey := clientPrivKey.PubKey().SerializeCompressed()
	assert.Equal(t, wantPubkey, mustDecodeHexT(t, call.pubkey))
}

// TestUnsubscribeNotifications_StoreErrorReturnsInternalError mirrors the
// subscribe-side equivalent above.
func TestUnsubscribeNotifications_StoreErrorReturnsInternalError(t *testing.T) {
	f := newServerTestFixture(t)
	f.store.unsubscribeErr = assert.AnError
	clientPrivKey, err := btcec.NewPrivateKey()
	require.NoError(t, err)

	blob := f.buildEncryptedRequest(t, clientPrivKey, "https://client.example/webhook", "", true)
	ctx := rpc.WithNode(t.Context(), &core.Node{EciesPrivateKey: f.nodeKey}, "token")

	_, err = f.srv.UnsubscribeNotifications(ctx, &pb.EncryptedNotificationRequest{Blob: blob})
	assert.ErrorIs(t, err, ErrInternal)
}

func mustDecodeHexT(t *testing.T, hexStr string) []byte {
	t.Helper()
	b, err := hex.DecodeString(hexStr)
	require.NoError(t, err)
	return b
}
