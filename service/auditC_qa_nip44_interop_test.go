package service

import (
	"strings"
	"testing"

	gonostr "github.com/nbd-wtf/go-nostr"
	gonip44 "github.com/nbd-wtf/go-nostr/nip44"
	"github.com/stretchr/testify/require"

	nmnip44 "github.com/ohstr/nmilat/nip44"
	"github.com/ohstr/nmilat/nipcash/transport"
)

// AUDIT-C QA: the private transport's two halves use two DIFFERENT NIP-44
// implementations, and nothing crossed the boundary in a test.
//
//	hub  (this repo)      github.com/nbd-wtf/go-nostr/nip44
//	                        private_transport.go:122,127  (unwrap the request)
//	                        private_dispatch.go:8          (encrypt the reply)
//	client (nmilat)       github.com/ohstr/nmilat/nip44
//	                        nipcash/transport/request.go:78,88 (wrap the request)
//	                        nipcash/client/batch_send.go:184   (decrypt the reply)
//
// Before this file, `ohstr/nmilat/nip44` was imported by ZERO files in lokihub,
// and every test on either side used ONE implementation for both halves:
// nmilat's fake hub encrypts with nmilat's nip44 (batch_e2e_test.go:145), and
// this repo's loop tests use go-nostr's on both sides (private_full_loop_test.go).
// So the production pair was exercised only by the live integration suite.
//
// nmilat's own conformance guarantee for NIP-44 is an EMPTY FUNCTION:
//
//	nip44/encryption_test.go:72
//	  func TestVectors(t *testing.T) {
//	  	// If we had official NIP-44 vectors we would put them here.
//	  	// For now, functional correctness is enough.
//	  }
//
// Its other three tests are Encrypt->Decrypt round trips through that same
// implementation, which cannot see a wire change. Three mutations applied to
// nmilat's nip44, each of which breaks interop with go-nostr and each of which
// left `go test ./nip44/...` fully GREEN:
//
//	Version = 2 -> 3                               (version byte)
//	hmacAad: Write(aad),Write(msg) -> swapped       (MAC input order)
//	pad length prefix big-endian -> little-endian   (padding layout)
//
// This test is what makes those visible: it is the only place the two
// implementations meet.

// auditCQANIP44Keys returns one conversation key computed independently by each
// library, asserting they agree — the precondition for everything below.
func auditCQANIP44Keys(t *testing.T) ([32]byte, string, string) {
	t.Helper()
	clientSK := gonostr.GeneratePrivateKey()
	clientPK, err := gonostr.GetPublicKey(clientSK)
	require.NoError(t, err)
	inboxSK := gonostr.GeneratePrivateKey()

	// The hub's side, exactly as private_transport.go:122 computes it.
	hubKey, err := gonip44.GenerateConversationKey(clientPK, inboxSK)
	require.NoError(t, err)

	// The client's side, via nmilat's own helper — which wraps nmilat's nip44
	// GenerateConversationKey. If these two ever diverge, no envelope decrypts
	// and no reply decrypts, in either direction.
	inboxPK, err := gonostr.GetPublicKey(inboxSK)
	require.NoError(t, err)
	clientKey, err := transport.ConversationKeyFor(inboxPK, clientSK)
	require.NoError(t, err)

	require.Equal(t, hubKey, clientKey,
		"the hub (go-nostr) and the client (nmilat) derived DIFFERENT conversation keys "+
			"for the same pair; every private-transport envelope would be undecryptable")
	return hubKey, clientSK, inboxSK
}

// TestAuditCQA_NIP44Interop_HubEncryptsClientDecrypts is the REPLY path: the hub
// seals a reply with go-nostr and the client opens it with nmilat.
//
// This is the direction that matters most, because by the time the reply is
// published the items have already RUN (private_dispatch.go:50-58). A divergence
// here is money moved and a caller who cannot be told.
func TestAuditCQA_NIP44Interop_HubEncryptsClientDecrypts(t *testing.T) {
	key, _, _ := auditCQANIP44Keys(t)

	// Lengths chosen to cross NIP-44's padding boundaries, which is where a
	// layout divergence shows up rather than in a single convenient size.
	for _, n := range []int{1, 31, 32, 33, 63, 64, 65, 255, 256, 257, 1023, 1024, 4096} {
		plaintext := strings.Repeat("x", n)

		sealed, err := gonip44.Encrypt(plaintext, key)
		require.NoError(t, err, "hub-side encrypt of %d bytes", n)

		opened, err := nmnip44.Decrypt(sealed, key[:])
		require.NoErrorf(t, err,
			"the client (nmilat/nip44) could not open a reply the hub (go-nostr/nip44) sealed, "+
				"at %d bytes. The two implementations have diverged on the wire format; every "+
				"private-transport reply is lost AFTER the items ran.", n)
		require.Equal(t, plaintext, opened, "round trip corrupted %d bytes", n)
	}
}

// TestAuditCQA_NIP44Interop_ClientEncryptsHubDecrypts is the REQUEST path:
// WrapRequest seals with nmilat, private_transport.go:127 opens with go-nostr.
// A divergence here fails closed — the hub omits everything and the caller reads
// silence — which is safer but still a total outage of all four bill methods.
func TestAuditCQA_NIP44Interop_ClientEncryptsHubDecrypts(t *testing.T) {
	key, _, _ := auditCQANIP44Keys(t)

	for _, n := range []int{1, 31, 32, 33, 63, 64, 65, 255, 256, 257, 1023, 1024, 4096} {
		plaintext := strings.Repeat("y", n)

		sealed, err := nmnip44.Encrypt(plaintext, key[:])
		require.NoError(t, err, "client-side encrypt of %d bytes", n)

		opened, err := gonip44.Decrypt(sealed, key)
		require.NoErrorf(t, err,
			"the hub (go-nostr/nip44) could not open an envelope the client (nmilat/nip44) "+
				"sealed, at %d bytes; the private transport is unusable in the request "+
				"direction", n)
		require.Equal(t, plaintext, opened, "round trip corrupted %d bytes", n)
	}
}

// TestAuditCQA_NIP44Interop_PaddedSizesAgree pins the padding LAYOUT rather than
// just decryptability.
//
// Padding is what the transport's own pad buckets are layered on top of
// (transport.Limits.PadBucketBytes exists to hide batch size), so if the two
// libraries padded differently the observable ciphertext length would depend on
// which side sealed it — and the bucket analysis that sized PadBucketBytes,
// which was done against nmilat's padding, would not describe the hub's replies.
func TestAuditCQA_NIP44Interop_PaddedSizesAgree(t *testing.T) {
	key, _, _ := auditCQANIP44Keys(t)

	for _, n := range []int{1, 32, 33, 100, 500, 1024, 8192} {
		plaintext := strings.Repeat("z", n)

		byHub, err := gonip44.Encrypt(plaintext, key)
		require.NoError(t, err)
		byClient, err := nmnip44.Encrypt(plaintext, key[:])
		require.NoError(t, err)

		require.Equalf(t, len(byHub), len(byClient),
			"a %d-byte payload seals to %d chars on the hub and %d on the client; the two "+
				"NIP-44 implementations pad differently, so ciphertext length leaks WHICH "+
				"side sent it and the PadBucketBytes analysis no longer holds",
			n, len(byHub), len(byClient))
	}
}
