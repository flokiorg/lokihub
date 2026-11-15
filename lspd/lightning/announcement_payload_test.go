package lightning

import (
	"encoding/hex"
	"errors"
	"testing"

	"github.com/nbd-wtf/go-nostr"
)

// specExample is the example event of NIP-LSP §The Announcement. Its pubkey
// is the x-only key of private key 1 (even Y, node id 02‖pubkey).
func specExample() *nostr.Event {
	return &nostr.Event{
		Kind:      KindLspAnnouncement,
		PubKey:    "79be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798",
		CreatedAt: 1792713600,
		Content:   "Inbound channels and just-in-time channels.",
		Tags: nostr.Tags{
			{"addr", "vww6ybal4bd7szmgncyruucpgfkqahzddi37ktceo3ah7ngmcopnpyyd.onion:9735"},
			{"addr", "203.0.113.7:9735"},
			{"n", "bitcoin"},
			{"network", "mainnet"},
			{"status", "open"},
			{"expiration", "1795305600"},
			{"protocol", "1"},
			{"protocol", "2"},
			{"protocol", "5"},
			{"lsps1", "min_required_channel_confirmations", "0"},
			{"lsps1", "min_funding_confirms_within_blocks", "6"},
			{"lsps1", "supports_zero_channel_reserve", "false"},
			{"lsps1", "max_channel_expiry_blocks", "12960"},
			{"lsps1", "min_initial_client_balance", "0"},
			{"lsps1", "max_initial_client_balance", "1000000"},
			{"lsps1", "min_initial_lsp_balance", "0"},
			{"lsps1", "max_initial_lsp_balance", "10000000"},
			{"lsps1", "min_channel_balance", "100000"},
			{"lsps1", "max_channel_balance", "10000000"},
			{"lsps2_fee", "2000000", "4000", "4320", "2016", "10000000", "4000000000"},
			{"lsps5", "max_webhooks", "10"},
			{"title", "Alice's Liquidity"},
			{"website", "https://lsp.example.com"},
			{"p", "c6047f9441ed7d6d3045406e95c07cd85c778e4b8cef3ca7abac09b95c709ee5", "wss://relay.example.com"},
			{"L", "ISO-639-1"},
			{"l", "en", "ISO-639-1"},
			{"alt", "Lightning service provider: Alice's Liquidity"},
			{"r", "wss://relay.example.com"},
		},
	}
}

func specNodeID(t *testing.T) []byte {
	t.Helper()
	id, err := hex.DecodeString("02" + specExample().PubKey)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestNewAnnouncementPayload_SpecExample(t *testing.T) {
	ev := specExample()
	p, err := NewAnnouncementPayload(ev.Serialize(), specNodeID(t))
	if err != nil {
		t.Fatalf("spec example refused: %v", err)
	}
	const wantID = "d87841c795e01ba75208a603bc0bdfe7f2015ab3876860501e2f09d0c257a45b"
	if got := p.ID(); got != wantID {
		t.Fatalf("id = %s, want %s (NIP-LSP example)", got, wantID)
	}
	if string(p.Bytes()) != string(ev.Serialize()) {
		t.Fatal("Bytes() differs from the serialization")
	}
}

func TestNewAnnouncementPayload_OddYNodeIDAccepted(t *testing.T) {
	ev := specExample()
	nodeID, _ := hex.DecodeString("03" + ev.PubKey)
	if _, err := NewAnnouncementPayload(ev.Serialize(), nodeID); err != nil {
		t.Fatalf("03-prefixed node id refused: %v", err)
	}
}

func TestNewAnnouncementPayload_Refusals(t *testing.T) {
	nodeID := specNodeID(t)

	otherKind := specExample()
	otherKind.Kind = 1
	zapRequest := specExample()
	zapRequest.Kind = 9734
	otherAuthor := specExample()
	otherAuthor.PubKey = "c6047f9441ed7d6d3045406e95c07cd85c778e4b8cef3ca7abac09b95c709ee5"

	canonical := string(specExample().Serialize())
	spaced := "[0, " + canonical[3:]

	cases := map[string][]byte{
		"kind 1":                 otherKind.Serialize(),
		"kind 9734":              zapRequest.Serialize(),
		"other author":           otherAuthor.Serialize(),
		"non-canonical spacing":  []byte(spaced),
		"not json":               []byte("Lightning Signed Message:hello"),
		"empty":                  nil,
		"object not array":       []byte(`{"kind":10735}`),
		"five elements":          []byte(`[0,"79be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798",1,10735,[]]`),
		"first element not zero": []byte(`[1,"79be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798",1,10735,[],""]`),
		"kind as string":         []byte(`[0,"79be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798",1,"10735",[],""]`),
		"trailing data":          []byte(canonical + " "),
	}
	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := NewAnnouncementPayload(payload, nodeID)
			if !errors.Is(err, ErrNotAnnouncementPayload) {
				t.Fatalf("err = %v, want ErrNotAnnouncementPayload", err)
			}
		})
	}
}

func TestNewAnnouncementPayload_BadNodeID(t *testing.T) {
	for _, id := range [][]byte{nil, make([]byte, 32), append([]byte{0x04}, make([]byte, 32)...)} {
		if _, err := NewAnnouncementPayload(specExample().Serialize(), id); err == nil {
			t.Fatalf("node id %x accepted", id)
		}
	}
}

func TestAnnouncementPayload_ZeroValue(t *testing.T) {
	var p AnnouncementPayload
	if !p.IsZero() {
		t.Fatal("zero value not IsZero")
	}
}
