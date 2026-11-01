package lightning

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/nbd-wtf/go-nostr"
)

// KindLspAnnouncement is the NIP-LSP announcement kind (Lightning Service
// Provider Announcement, a replaceable event signed by the node key).
const KindLspAnnouncement = 10735

// ErrNotAnnouncementPayload is returned for any payload the node key must
// not sign: anything other than the NIP-01 serialization of a kind-10735
// event authored by that same node.
var ErrNotAnnouncementPayload = errors.New("payload is not a kind-10735 announcement of this node")

// AnnouncementPayload is a NIP-01 event serialization that has been checked
// to be a kind-10735 announcement whose pubkey is the node's own x-only key.
// Its field is unexported, so only NewAnnouncementPayload can build a
// non-zero one: a NodeKeySigner accepts nothing else, which is how flspd
// keeps every other message away from the node key's Schnorr signer
// (NIP-LSP, Security Considerations: "A signer SHOULD sign only this kind of
// Nostr event with it").
type AnnouncementPayload struct {
	serialization []byte
}

// NewAnnouncementPayload checks serialization and wraps it.
//
// serialization must be exactly the NIP-01 form
// [0,<pubkey>,<created_at>,<kind>,<tags>,<content>] of an event with kind
// 10735 and pubkey equal to the x-only form of nodeID (the 33-byte
// compressed node id). The bytes are compared against a canonical
// re-serialization, so a payload produced by any NIP-01 encoder (the event
// builder in T8-3) is accepted only when it is the exact preimage of the
// event id a relay or client will recompute.
func NewAnnouncementPayload(serialization []byte, nodeID []byte) (AnnouncementPayload, error) {
	if len(nodeID) != 33 || (nodeID[0] != 0x02 && nodeID[0] != 0x03) {
		return AnnouncementPayload{}, fmt.Errorf("node id must be a 33-byte compressed public key")
	}
	wantPubkey := hex.EncodeToString(nodeID[1:])

	var raw []json.RawMessage
	if err := json.Unmarshal(serialization, &raw); err != nil || len(raw) != 6 {
		return AnnouncementPayload{}, fmt.Errorf("%w: not a NIP-01 serialization", ErrNotAnnouncementPayload)
	}

	var (
		zero      int
		pubkey    string
		createdAt int64
		kind      int
		tags      nostr.Tags
		content   string
	)
	fields := []any{&zero, &pubkey, &createdAt, &kind, &tags, &content}
	for i, f := range fields {
		if err := json.Unmarshal(raw[i], f); err != nil {
			return AnnouncementPayload{}, fmt.Errorf("%w: field %d: %v", ErrNotAnnouncementPayload, i, err)
		}
	}
	if zero != 0 {
		return AnnouncementPayload{}, fmt.Errorf("%w: first element must be 0", ErrNotAnnouncementPayload)
	}
	if kind != KindLspAnnouncement {
		return AnnouncementPayload{}, fmt.Errorf("%w: kind %d", ErrNotAnnouncementPayload, kind)
	}
	if pubkey != wantPubkey {
		return AnnouncementPayload{}, fmt.Errorf("%w: pubkey %q is not the node key", ErrNotAnnouncementPayload, pubkey)
	}
	if createdAt < 0 {
		return AnnouncementPayload{}, fmt.Errorf("%w: negative created_at", ErrNotAnnouncementPayload)
	}
	if tags == nil {
		tags = nostr.Tags{}
	}

	canonical := (&nostr.Event{
		PubKey:    pubkey,
		CreatedAt: nostr.Timestamp(createdAt),
		Kind:      kind,
		Tags:      tags,
		Content:   content,
	}).Serialize()
	if !bytes.Equal(canonical, serialization) {
		return AnnouncementPayload{}, fmt.Errorf("%w: not the canonical NIP-01 serialization", ErrNotAnnouncementPayload)
	}

	return AnnouncementPayload{serialization: append([]byte(nil), serialization...)}, nil
}

// Bytes returns the checked serialization. It is what the node key signs:
// flnd hashes it once with SHA-256, which is the Nostr event id.
func (p AnnouncementPayload) Bytes() []byte {
	return append([]byte(nil), p.serialization...)
}

// IsZero reports whether p was not built by NewAnnouncementPayload.
func (p AnnouncementPayload) IsZero() bool {
	return len(p.serialization) == 0
}

// ID returns the Nostr event id: lowercase hex SHA-256 of the serialization.
func (p AnnouncementPayload) ID() string {
	sum := sha256.Sum256(p.serialization)
	return hex.EncodeToString(sum[:])
}

// NodeKeySigner produces a BIP-340 Schnorr signature with the Lightning
// node's own key (flnd signrpc SignMessage, key family 6 index 0, single
// SHA-256 of the message). It takes only an AnnouncementPayload, never raw
// bytes. Implemented by the flnd adapter.
type NodeKeySigner interface {
	SignAnnouncementPayload(ctx context.Context, payload AnnouncementPayload) ([]byte, error)
}
