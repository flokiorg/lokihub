package ports

import "context"

// AnnouncementSigner signs NIP-LSP announcements (kind 10735) with one
// node's Lightning node key (T8-1).
//
// SignPayload has the shape of the node signer the event builder takes
// (`func(ctx, payload) (sig, error)`): payload is the NIP-01 serialization
// of an unsigned kind-10735 event whose pubkey is PubKey(), and the result
// is the 64-byte BIP-340 signature over SHA-256(payload), which is the
// event id. Any other payload is refused before it reaches flnd.
type AnnouncementSigner interface {
	// PubKey is the node key in its 32-byte x-only form, lowercase hex:
	// the announcement's pubkey.
	PubKey() string
	SignPayload(ctx context.Context, payload []byte) ([]byte, error)
}
