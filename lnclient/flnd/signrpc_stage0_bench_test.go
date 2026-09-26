//go:build flndbench

// Package flnd's Stage 0 gate for the private-hub transport.
//
// The private transport addresses gift wraps to the hub's Lightning node
// identity key. If the node keeps that private key (as it should), then every
// inbound envelope needs one ECDH against it — and because the wrap sender is a
// fresh ephemeral key every time, NOTHING IS CACHEABLE. So every envelope,
// including junk an attacker sprays at a publicly-recoverable inbox pubkey,
// costs a gRPC round trip into flnd.
//
// This measures whether that is affordable. The decision it feeds:
//
//	per-call cost > ~200 µs  =>  do NOT put the node on the hot path.
//	                            Derive a transport subkey from the hub seed and
//	                            publish a node-signed announcement instead.
//
// It also measures the in-process costs the cost model assumes (NIP-44 open at
// realistic envelope sizes, schnorr verify), because those figures were taken at
// 1 KiB and the batch envelope pads to 4 KiB and up.
//
// Run against the dev stack's flnd:
//
//	FLND_ADDRESS=localhost:10005 \
//	FLND_MACAROON_FILE=./data/flnd/data/chain/flokicoin/main/admin.macaroon \
//	go test -tags flndbench ./lnclient/flnd/ -run TestStage0 -v
//
// Paths are relative to the repo root, so pass an absolute path when running
// from a worktree whose data/ directory is not populated. The dev stack's flnd
// is only reachable from inside its docker network, so in practice:
//
//	docker exec lokihub-dev-backend sh -c 'cd /app && FLND_ADDRESS=flnd:10005 \
//	  FLND_MACAROON_FILE=/flnd-data/data/chain/flokicoin/main/admin.macaroon \
//	  GOWORK=off go test -tags flndbench ./lnclient/flnd/ -run TestStage0 -v'
//
// # MEASURED 2026-09-26, dev stack flnd v0.2.2-beta, one core
//
//	operation                                p50        p99        verdict
//	-----------------------------------------------------------------------------
//	signrpc.DeriveSharedKey (node)           602 µs     1.36 ms    GATE FAILED
//	signrpc.SignMessage{schnorr} (node)      1.15 ms    1.62 ms    fine (per restart)
//	nip44 conversation key (in-process)      166 µs     519 µs     as estimated
//	nip44 open @1 KiB                        5.3 µs     20 µs      2x the 2.6 µs estimate
//	nip44 open @4 KiB                        22 µs      735 µs
//	nip44 open @16 KiB                       124 µs     379 µs
//	nip44 open @32 KiB                       277 µs     756 µs
//	schnorr verify (per item proof)          373 µs     592 µs     2.3x the 161 µs estimate
//
// # CONCLUSIONS
//
//  1. THE GATE FAILED. 602 µs against a 200 µs threshold, and 3.6x the 166 µs
//     the same ECDH costs in-process — the extra ~440 µs is pure gRPC/IPC. Since
//     the wrap sender is a fresh ephemeral key, nothing is cacheable, so this
//     would be paid on every inbound envelope AND every piece of junk. The node
//     must stay OFF the hot path: derive a transport subkey from the hub seed and
//     publish a node-signed announcement instead. The raw-x ECDH flnd PR is
//     therefore NOT needed.
//
//  2. Signing the announcement is cheap enough at 1.15 ms, because it happens
//     once per restart rather than per event. That is the fallback's only node
//     dependency.
//
//  3. shared_key came back 32 bytes = sha256(compressed point), confirming
//     DeriveSharedKey cannot feed NIP-44's HKDF even if latency were free.
//
//  4. NIP-44 HAS A HARD 65535-BYTE PLAINTEXT CEILING (65535 encrypts, 65536
//     errors "plaintext should be between 1b and 64kB"). The design's 256 KiB
//     padded envelope is impossible. Worse, a kind-23192 proof event alone is
//     ~500-600 bytes of JSON, so ~800-1000 bytes per item realistically — which
//     puts 100 items at 80-100 KB, ABOVE the cap. The item cap must be derived
//     from this ceiling, not copied from maxConsolidateSources=100.
//
//  5. Per-item cost is worse than modelled: 373 µs of schnorr verify, not 161 µs.
//     A 60-item envelope is ~25 ms of verification alone, so the realistic figure
//     is ~2.4k items/s/core rather than 3.9k. Verification must be parallelised
//     across items (it is pure CPU and independent), and the envelope deadline
//     has to account for it.
package flnd

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/flokiorg/flnd/lnrpc"
	"github.com/flokiorg/flnd/lnrpc/signrpc"
	"github.com/flokiorg/flnd/macaroons"
	"github.com/flokiorg/go-flokicoin/crypto"
	"github.com/flokiorg/go-flokicoin/crypto/schnorr"
	"github.com/nbd-wtf/go-nostr/nip44"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"gopkg.in/macaroon.v2"
)

// stage0Samples is how many calls each latency figure is taken over. Large
// enough for a believable p99, small enough not to hammer a dev node.
const stage0Samples = 300

func dialStage0(t *testing.T) (*grpc.ClientConn, string) {
	t.Helper()

	addr := os.Getenv("FLND_ADDRESS")
	if addr == "" {
		t.Skip("FLND_ADDRESS not set; this benchmark needs a real flnd")
	}
	macPath := os.Getenv("FLND_MACAROON_FILE")
	if macPath == "" {
		t.Skip("FLND_MACAROON_FILE not set; signrpc calls need an admin macaroon")
	}

	macBytes, err := os.ReadFile(macPath)
	if err != nil {
		t.Fatalf("read macaroon: %v", err)
	}
	mac := &macaroon.Macaroon{}
	if err := mac.UnmarshalBinary(macBytes); err != nil {
		t.Fatalf("unmarshal macaroon: %v", err)
	}
	macCred, err := macaroons.NewMacaroonCredential(mac)
	if err != nil {
		t.Fatalf("macaroon credential: %v", err)
	}

	conn, err := grpc.NewClient(addr,
		grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{InsecureSkipVerify: true})), //nolint:gosec // dev node
		grpc.WithPerRPCCredentials(macCred),
	)
	if err != nil {
		t.Fatalf("dial flnd: %v", err)
	}

	info, err := lnrpc.NewLightningClient(conn).GetInfo(context.Background(), &lnrpc.GetInfoRequest{})
	if err != nil {
		t.Fatalf("GetInfo (is flnd reachable?): %v", err)
	}
	return conn, info.IdentityPubkey
}

// report prints p50/p99/mean and the implied single-core sustained rate.
func report(t *testing.T, label string, d []time.Duration) {
	t.Helper()
	sort.Slice(d, func(i, j int) bool { return d[i] < d[j] })
	var total time.Duration
	for _, x := range d {
		total += x
	}
	mean := total / time.Duration(len(d))
	p50 := d[len(d)*50/100]
	p99 := d[len(d)*99/100]
	t.Logf("%-38s n=%d  p50=%-12v p99=%-12v mean=%-12v  => %.0f calls/s serial",
		label, len(d), p50, p99, mean, float64(time.Second)/float64(mean))
}

// TestStage0_NodeHotPathCost is the gate. It measures the two node RPCs the
// private transport would depend on, each with FRESH key material every call so
// no server-side cache is doing us a favour we would not get in production.
func TestStage0_NodeHotPathCost(t *testing.T) {
	conn, nodePubkey := dialStage0(t)
	defer conn.Close()
	t.Logf("flnd identity pubkey: %s", nodePubkey)

	signer := signrpc.NewSignerClient(conn)
	ctx := context.Background()

	// --- 1. DeriveSharedKey, fresh ephemeral key per call (the real pattern) ---
	//
	// If this errors with Unimplemented, signrpc is not compiled into this flnd
	// build; if it errors on permissions, the macaroon lacks signer:generate.
	// Both are findings, not test failures to paper over.
	ecdh := make([]time.Duration, 0, stage0Samples)
	for i := 0; i < stage0Samples; i++ {
		eph, err := crypto.NewPrivateKey()
		if err != nil {
			t.Fatalf("ephemeral key: %v", err)
		}
		start := time.Now()
		resp, err := signer.DeriveSharedKey(ctx, &signrpc.SharedKeyRequest{
			EphemeralPubkey: eph.PubKey().SerializeCompressed(),
		})
		elapsed := time.Since(start)
		if err != nil {
			t.Fatalf("DeriveSharedKey (call %d): %v", i, err)
		}
		if i == 0 {
			t.Logf("shared_key is %d bytes (sha256 of the compressed point — NOT the raw x NIP-44 needs)",
				len(resp.SharedKey))
		}
		ecdh = append(ecdh, elapsed)
	}
	report(t, "signrpc.DeriveSharedKey (node)", ecdh)

	// --- 2. SignMessage with schnorr, for the announcement-signing fallback ---
	schnorrSig := make([]time.Duration, 0, stage0Samples)
	msg := make([]byte, 32)
	for i := 0; i < stage0Samples; i++ {
		if _, err := rand.Read(msg); err != nil {
			t.Fatalf("rand: %v", err)
		}
		start := time.Now()
		_, err := signer.SignMessage(ctx, &signrpc.SignMessageReq{
			Msg:        msg,
			KeyLoc:     &signrpc.KeyLocator{KeyFamily: 6, KeyIndex: 0}, // KeyFamilyNodeKey
			SchnorrSig: true,
		})
		elapsed := time.Since(start)
		if err != nil {
			t.Fatalf("SignMessage schnorr (call %d): %v", i, err)
		}
		schnorrSig = append(schnorrSig, elapsed)
	}
	report(t, "signrpc.SignMessage{schnorr} (node)", schnorrSig)

	t.Log("GATE: if DeriveSharedKey p50 > ~200us, keep the node OFF the hot path " +
		"(transport subkey + node-signed announcement).")
}

// TestStage0_InProcessCrypto measures what the cost model assumes for the
// alternative where lokihub holds the transport key: in-process ECDH, NIP-44
// open at the sizes a padded batch envelope actually reaches, and the schnorr
// verify paid per item proof.
func TestStage0_InProcessCrypto(t *testing.T) {
	priv, err := crypto.NewPrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	peer, err := crypto.NewPrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	privHex := hex.EncodeToString(priv.Serialize())
	peerXOnly := hex.EncodeToString(schnorr.SerializePubKey(peer.PubKey()))

	// In-process ECDH (NIP-44 conversation key).
	conv := make([]time.Duration, 0, stage0Samples)
	for i := 0; i < stage0Samples; i++ {
		start := time.Now()
		if _, err := nip44.GenerateConversationKey(peerXOnly, privHex); err != nil {
			t.Fatalf("conversation key: %v", err)
		}
		conv = append(conv, time.Since(start))
	}
	report(t, "nip44 conversation key (in-process ECDH)", conv)

	ck, err := nip44.GenerateConversationKey(peerXOnly, privHex)
	if err != nil {
		t.Fatal(err)
	}

	// NIP-44 has a HARD plaintext ceiling — go-nostr rejects anything over
	// 65535 bytes ("plaintext should be between 1b and 64kB"). That caps the
	// batch envelope's padded size, so probe it explicitly rather than
	// discovering it in production as a silent publish failure.
	for _, size := range []int{65535, 65536} {
		plaintext := make([]byte, size)
		_, err := nip44.Encrypt(string(plaintext), [32]byte{})
		t.Logf("nip44 encrypt @%d bytes: err=%v", size, err)
	}

	// NIP-44 open across the padded envelope sizes the design can actually use.
	for _, size := range []int{1 << 10, 4 << 10, 16 << 10, 32 << 10} {
		plaintext := make([]byte, size)
		if _, err := rand.Read(plaintext); err != nil {
			t.Fatal(err)
		}
		ciphertext, err := nip44.Encrypt(string(plaintext), ck)
		if err != nil {
			t.Fatalf("encrypt %d: %v", size, err)
		}
		open := make([]time.Duration, 0, 100)
		for i := 0; i < 100; i++ {
			start := time.Now()
			if _, err := nip44.Decrypt(ciphertext, ck); err != nil {
				t.Fatalf("decrypt %d: %v", size, err)
			}
			open = append(open, time.Since(start))
		}
		report(t, fmt.Sprintf("nip44 open @%4d KiB", size>>10), open)
	}

	// Schnorr verify — paid once per item proof, so this multiplies by batch size.
	digest := sha256.Sum256([]byte("stage0 item proof digest"))
	sig, err := schnorr.Sign(priv, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	pub := priv.PubKey()
	verify := make([]time.Duration, 0, stage0Samples)
	for i := 0; i < stage0Samples; i++ {
		start := time.Now()
		if !sig.Verify(digest[:], pub) {
			t.Fatal("signature did not verify")
		}
		verify = append(verify, time.Since(start))
	}
	report(t, "schnorr verify (per item proof)", verify)
}
