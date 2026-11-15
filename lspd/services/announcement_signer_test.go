package services

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"

	btcec "github.com/flokiorg/go-flokicoin/crypto"
	"github.com/flokiorg/go-flokicoin/crypto/schnorr"
	"github.com/flokiorg/lokihub/lspd/core"
	"github.com/flokiorg/lokihub/lspd/lightning"
	"github.com/nbd-wtf/go-nostr"
)

// fakeNodeKeySigner signs like flnd's signrpc SignMessage with schnorr_sig
// set: BIP-340 over SHA-256(msg), with priv.
type fakeNodeKeySigner struct {
	priv  *btcec.PrivateKey
	calls int
}

func (f *fakeNodeKeySigner) SignAnnouncementPayload(_ context.Context, p lightning.AnnouncementPayload) ([]byte, error) {
	f.calls++
	digest := sha256.Sum256(p.Bytes())
	sig, err := schnorr.Sign(f.priv, digest[:])
	if err != nil {
		return nil, err
	}
	return sig.Serialize(), nil
}

func newTestNode(t *testing.T, signer lightning.NodeKeySigner, priv *btcec.PrivateKey) *core.Node {
	t.Helper()
	return &core.Node{
		NodeId:        priv.PubKey().SerializeCompressed(),
		NodeKeySigner: signer,
	}
}

// keyWithPrefix returns a private key whose compressed public key starts
// with prefix (0x02 or 0x03), so both node id forms are exercised.
func keyWithPrefix(t *testing.T, prefix byte) *btcec.PrivateKey {
	t.Helper()
	for i := byte(1); i < 255; i++ {
		seed := sha256.Sum256([]byte{prefix, i})
		priv, _ := btcec.PrivKeyFromBytes(seed[:])
		if priv.PubKey().SerializeCompressed()[0] == prefix {
			return priv
		}
	}
	t.Fatal("no key found")
	return nil
}

func unsignedAnnouncement(pubkey string) *nostr.Event {
	return &nostr.Event{
		Kind:      lightning.KindLspAnnouncement,
		PubKey:    pubkey,
		CreatedAt: 1792713600,
		Content:   "",
		Tags: nostr.Tags{
			{"addr", "203.0.113.7:9735"},
			{"n", "flokicoin"},
			{"network", "regtest"},
			{"status", "open"},
			{"expiration", "1795305600"},
			{"protocol", "1"},
		},
	}
}

func TestAnnouncementSigner_SignaturesVerifyWithNostr(t *testing.T) {
	for _, prefix := range []byte{0x02, 0x03} {
		priv := keyWithPrefix(t, prefix)
		fake := &fakeNodeKeySigner{priv: priv}
		s, err := NewAnnouncementSigner(newTestNode(t, fake, priv))
		if err != nil {
			t.Fatal(err)
		}
		if want := hex.EncodeToString(priv.PubKey().SerializeCompressed()[1:]); s.PubKey() != want {
			t.Fatalf("PubKey = %s, want %s", s.PubKey(), want)
		}

		ev := unsignedAnnouncement(s.PubKey())
		sig, err := s.SignPayload(context.Background(), ev.Serialize())
		if err != nil {
			t.Fatalf("prefix %02x: %v", prefix, err)
		}
		ev.ID = ev.GetID()
		ev.Sig = hex.EncodeToString(sig)
		ok, err := ev.CheckSignature()
		if err != nil || !ok {
			t.Fatalf("prefix %02x: go-nostr rejects the signature: ok=%v err=%v", prefix, ok, err)
		}
	}
}

func TestAnnouncementSigner_RefusesOtherPayloadsBeforeFlnd(t *testing.T) {
	priv := keyWithPrefix(t, 0x02)
	fake := &fakeNodeKeySigner{priv: priv}
	s, err := NewAnnouncementSigner(newTestNode(t, fake, priv))
	if err != nil {
		t.Fatal(err)
	}

	note := unsignedAnnouncement(s.PubKey())
	note.Kind = 1
	someoneElse := unsignedAnnouncement("c6047f9441ed7d6d3045406e95c07cd85c778e4b8cef3ca7abac09b95c709ee5")

	for name, payload := range map[string][]byte{
		"kind 1":       note.Serialize(),
		"other author": someoneElse.Serialize(),
		"raw message":  []byte("LSPS5: DO NOT SIGN THIS MESSAGE MANUALLY"),
	} {
		if _, err := s.SignPayload(context.Background(), payload); !errors.Is(err, lightning.ErrNotAnnouncementPayload) {
			t.Fatalf("%s: err = %v, want ErrNotAnnouncementPayload", name, err)
		}
	}
	if fake.calls != 0 {
		t.Fatalf("node key signer called %d times for refused payloads", fake.calls)
	}
}

func TestAnnouncementSigner_RejectsSignatureByAnotherKey(t *testing.T) {
	nodeKey := keyWithPrefix(t, 0x02)
	wrongKey := keyWithPrefix(t, 0x03)
	s, err := NewAnnouncementSigner(newTestNode(t, &fakeNodeKeySigner{priv: wrongKey}, nodeKey))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SignPayload(context.Background(), unsignedAnnouncement(s.PubKey()).Serialize()); err == nil {
		t.Fatal("signature by another key accepted")
	}
}

type errSigner struct{ err error }

func (e errSigner) SignAnnouncementPayload(context.Context, lightning.AnnouncementPayload) ([]byte, error) {
	return nil, e.err
}

func TestAnnouncementSigner_PropagatesSignerError(t *testing.T) {
	priv := keyWithPrefix(t, 0x02)
	boom := errors.New("permission denied")
	s, _ := NewAnnouncementSigner(newTestNode(t, errSigner{boom}, priv))
	if _, err := s.SignPayload(context.Background(), unsignedAnnouncement(s.PubKey()).Serialize()); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
}

func TestNewAnnouncementSigner_Refusals(t *testing.T) {
	if _, err := NewAnnouncementSigner(nil); err == nil {
		t.Fatal("nil node accepted")
	}
	priv := keyWithPrefix(t, 0x02)
	if _, err := NewAnnouncementSigner(&core.Node{NodeId: priv.PubKey().SerializeCompressed()}); err == nil {
		t.Fatal("node without signer accepted")
	}
	if _, err := NewAnnouncementSigner(&core.Node{NodeId: []byte{2}, NodeKeySigner: errSigner{}}); err == nil {
		t.Fatal("short node id accepted")
	}
}
