package services

import (
	"context"
	"encoding/hex"
	"fmt"

	"github.com/flokiorg/go-flokicoin/crypto/schnorr"
	"github.com/flokiorg/lokihub/lspd/core"
	"github.com/flokiorg/lokihub/lspd/lightning"
	"github.com/flokiorg/lokihub/lspd/ports"
)

type announcementSigner struct {
	nodeID []byte
	pubkey string
	signer lightning.NodeKeySigner
}

var _ ports.AnnouncementSigner = (*announcementSigner)(nil)

// NewAnnouncementSigner returns the NIP-LSP announcement signer of node
// (T8-1). The node must carry a NodeKeySigner (its flnd client) and its
// 33-byte node id.
func NewAnnouncementSigner(node *core.Node) (ports.AnnouncementSigner, error) {
	if node == nil || node.NodeKeySigner == nil {
		return nil, fmt.Errorf("node has no node key signer")
	}
	if len(node.NodeId) != 33 {
		return nil, fmt.Errorf("node id must be 33 bytes, got %d", len(node.NodeId))
	}
	return &announcementSigner{
		nodeID: append([]byte(nil), node.NodeId...),
		pubkey: hex.EncodeToString(node.NodeId[1:]),
		signer: node.NodeKeySigner,
	}, nil
}

func (s *announcementSigner) PubKey() string { return s.pubkey }

// SignPayload checks payload (lightning.NewAnnouncementPayload: kind 10735,
// this node's pubkey, canonical NIP-01 form), has flnd sign it with the
// node key, and verifies the signature against the node key before
// returning it, so a wrong key locator or a misbehaving signer never yields
// an announcement that relays and clients would reject.
func (s *announcementSigner) SignPayload(ctx context.Context, payload []byte) ([]byte, error) {
	p, err := lightning.NewAnnouncementPayload(payload, s.nodeID)
	if err != nil {
		return nil, err
	}

	sigBytes, err := s.signer.SignAnnouncementPayload(ctx, p)
	if err != nil {
		return nil, err
	}

	sig, err := schnorr.ParseSignature(sigBytes)
	if err != nil {
		return nil, fmt.Errorf("node signer returned a malformed signature: %w", err)
	}
	pub, err := schnorr.ParsePubKey(s.nodeID[1:])
	if err != nil {
		return nil, fmt.Errorf("parsing node key: %w", err)
	}
	id, _ := hex.DecodeString(p.ID())
	if !sig.Verify(id, pub) {
		return nil, fmt.Errorf("node signer's signature does not verify against node key %s", s.pubkey)
	}
	return sigBytes, nil
}
