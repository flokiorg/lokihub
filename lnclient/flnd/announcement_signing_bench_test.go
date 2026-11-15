//go:build flndbench

// End-to-end check that the hub's private-transport announcement can actually be
// signed by the Lightning node. Shares dialStage0 with signrpc_stage0_bench_test.go;
// see that file's header for how to run these.
package flnd

import (
	"context"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/flokiorg/flnd/lnrpc/signrpc"
	"github.com/ohstr/nmilat/nipcash/transport"
)

// TestStage5_NodeProducesNostrValidAnnouncement proves the announcement is
// signable by the node, and pins the distinction that otherwise fails silently.
//
// flnd hashes the message itself, so a Nostr event must be signed by handing the
// node the event's SERIALIZATION, never its id. Pass the id and the node signs
// sha256(id) — a perfectly valid signature over the wrong digest, after which
// verification fails with nothing pointing at the cause.
//
// So this asserts both directions: the correct route verifies, and the tempting
// wrong route does not. If the wrong route ever started verifying, the distinction
// would have quietly disappeared and this test would say so.
func TestStage5_NodeProducesNostrValidAnnouncement(t *testing.T) {
	conn, nodePubkeyCompressed := dialStage0(t)
	defer conn.Close()

	signer := signrpc.NewSignerClient(conn)
	ctx := context.Background()

	// x-only form — exactly what a client derives from a bill's mint signature.
	nodeXOnly := nodePubkeyCompressed[2:]

	// A stand-in inbox key; this test is about the signature, not the key.
	ev, err := transport.NewAnnouncement(nodeXOnly, strings.Repeat("ab", 32), transport.DefaultLimits(), nil)
	if err != nil {
		t.Fatalf("NewAnnouncement: %v", err)
	}

	signWithNode := func(msg []byte) []byte {
		t.Helper()
		resp, err := signer.SignMessage(ctx, &signrpc.SignMessageReq{
			Msg:        msg,
			KeyLoc:     &signrpc.KeyLocator{KeyFamily: 6, KeyIndex: 0}, // KeyFamilyNodeKey
			SchnorrSig: true,
			DoubleHash: false,
		})
		if err != nil {
			t.Fatalf("node schnorr sign: %v", err)
		}
		return resp.Signature
	}

	// The correct route: hand the node the serialization.
	payload, err := transport.AnnouncementSigningPayload(ev)
	if err != nil {
		t.Fatal(err)
	}
	ev.Sig = hex.EncodeToString(signWithNode(payload))

	if _, err := transport.ParseAnnouncement(ev, nodeXOnly); err != nil {
		t.Fatalf("a node-signed announcement must verify: %v", err)
	}
	t.Logf("node signed a Nostr-valid announcement: pubkey=%s", nodeXOnly)

	// The tempting mistake: hand it the id instead.
	digest, err := transport.AnnouncementDigest(ev)
	if err != nil {
		t.Fatal(err)
	}
	ev.Sig = hex.EncodeToString(signWithNode(digest))

	if _, err := transport.ParseAnnouncement(ev, nodeXOnly); err == nil {
		t.Error("signing the id rather than its pre-image produced a valid announcement; " +
			"the distinction this test exists for has gone away")
	}
}
