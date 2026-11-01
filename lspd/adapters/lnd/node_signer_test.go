package lnd

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/flokiorg/flnd/lnrpc"
	"github.com/flokiorg/flnd/lnrpc/signrpc"
	"github.com/flokiorg/lokihub/lspd/lightning"
	"github.com/nbd-wtf/go-nostr"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"gopkg.in/macaroon-bakery.v2/bakery"
	"gopkg.in/macaroon.v2"
)

type fakeSignerClient struct {
	signrpc.SignerClient
	got  *signrpc.SignMessageReq
	resp *signrpc.SignMessageResp
	err  error
}

func (f *fakeSignerClient) SignMessage(_ context.Context, in *signrpc.SignMessageReq, _ ...grpc.CallOption) (*signrpc.SignMessageResp, error) {
	f.got = in
	return f.resp, f.err
}

const testPubkey = "79be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798"

func testPayload(t *testing.T) lightning.AnnouncementPayload {
	t.Helper()
	ev := &nostr.Event{Kind: lightning.KindLspAnnouncement, PubKey: testPubkey, CreatedAt: 1, Tags: nostr.Tags{}}
	nodeID, _ := hex.DecodeString("02" + testPubkey)
	p, err := lightning.NewAnnouncementPayload(ev.Serialize(), nodeID)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSignAnnouncementPayload_Request(t *testing.T) {
	fake := &fakeSignerClient{resp: &signrpc.SignMessageResp{Signature: make([]byte, 64)}}
	c := &LndClient{signerClient: fake}
	p := testPayload(t)

	if _, err := c.SignAnnouncementPayload(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	req := fake.got
	if string(req.Msg) != string(p.Bytes()) {
		t.Fatal("Msg is not the payload")
	}
	if !req.SchnorrSig || req.DoubleHash || req.CompactSig || len(req.Tag) != 0 || len(req.SchnorrSigTapTweak) != 0 {
		t.Fatalf("want plain BIP-340 over SHA-256(msg), got %+v", req)
	}
	if req.KeyLoc.GetKeyFamily() != 6 || req.KeyLoc.GetKeyIndex() != 0 {
		t.Fatalf("key locator = %v, want family 6 index 0 (node key)", req.KeyLoc)
	}
}

func TestSignAnnouncementPayload_Errors(t *testing.T) {
	c := &LndClient{signerClient: &fakeSignerClient{}}
	if _, err := c.SignAnnouncementPayload(context.Background(), lightning.AnnouncementPayload{}); !errors.Is(err, lightning.ErrNotAnnouncementPayload) {
		t.Fatalf("zero payload: err = %v", err)
	}

	denied := &LndClient{signerClient: &fakeSignerClient{err: status.Error(codes.PermissionDenied, "permission denied")}}
	_, err := denied.SignAnnouncementPayload(context.Background(), testPayload(t))
	if err == nil || !strings.Contains(err.Error(), "signer:generate") {
		t.Fatalf("permission denied: err = %v", err)
	}

	short := &LndClient{signerClient: &fakeSignerClient{resp: &signrpc.SignMessageResp{Signature: make([]byte, 70)}}}
	if _, err := short.SignAnnouncementPayload(context.Background(), testPayload(t)); err == nil {
		t.Fatal("70-byte signature accepted")
	}
}

// bakeTestMacaroon builds a macaroon the way flnd does: id = version byte
// followed by the protobuf MacaroonId carrying the permissions.
func bakeTestMacaroon(t *testing.T, ops ...*lnrpc.Op) string {
	t.Helper()
	idProto, err := proto.Marshal(&lnrpc.MacaroonId{Nonce: []byte("n"), StorageId: []byte("0"), Ops: ops})
	if err != nil {
		t.Fatal(err)
	}
	id := append([]byte{byte(bakery.LatestVersion)}, idProto...)
	mac, err := macaroon.New([]byte("root-key-root-key-root-key-12345"), id, "flnd", macaroon.LatestVersion)
	if err != nil {
		t.Fatal(err)
	}
	b, err := mac.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

func TestCheckSignerMacaroon(t *testing.T) {
	readOnly := &lnrpc.Op{Entity: "info", Actions: []string{"read"}}
	cases := []struct {
		name string
		mac  string
		ok   bool
	}{
		{"admin-like", bakeTestMacaroon(t, readOnly, &lnrpc.Op{Entity: "signer", Actions: []string{"read", "generate"}}), true},
		{"uri permission", bakeTestMacaroon(t, &lnrpc.Op{Entity: "uri", Actions: []string{SignMessageURI}}), true},
		{"signer read only", bakeTestMacaroon(t, &lnrpc.Op{Entity: "signer", Actions: []string{"read"}}), false},
		{"other uri", bakeTestMacaroon(t, &lnrpc.Op{Entity: "uri", Actions: []string{"/signrpc.Signer/SignOutputRaw"}}), false},
		{"no signer", bakeTestMacaroon(t, readOnly, &lnrpc.Op{Entity: "invoices", Actions: []string{"generate"}}), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckSignerMacaroon(tc.mac)
			if (err == nil) != tc.ok {
				t.Fatalf("err = %v, want ok=%v", err, tc.ok)
			}
		})
	}

	for name, bad := range map[string]string{"not hex": "zz", "not a macaroon": "00ff"} {
		if err := CheckSignerMacaroon(bad); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}
