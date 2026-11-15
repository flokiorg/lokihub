package lnd

import (
	"context"
	"fmt"

	"github.com/flokiorg/flnd/keychain"
	"github.com/flokiorg/flnd/lnrpc/signrpc"
	"github.com/flokiorg/lokihub/lspd/lightning"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// nodeKeyLocator is the node identity key: key family 6 (KeyFamilyNodeKey),
// index 0. Its public key is the node id.
var nodeKeyLocator = &signrpc.KeyLocator{
	KeyFamily: int32(keychain.KeyFamilyNodeKey),
	KeyIndex:  0,
}

var _ lightning.NodeKeySigner = (*LndClient)(nil)

// SignAnnouncementPayload signs a checked NIP-LSP announcement serialization
// with the node key through signrpc SignMessage. With schnorr_sig set and
// neither double_hash nor tag, flnd signs SHA-256(msg) with BIP-340, so the
// signed digest is exactly the Nostr event id and the 64-byte result is the
// event's sig. The macaroon must grant signer:generate.
func (c *LndClient) SignAnnouncementPayload(ctx context.Context, payload lightning.AnnouncementPayload) ([]byte, error) {
	if payload.IsZero() {
		return nil, lightning.ErrNotAnnouncementPayload
	}

	resp, err := c.signerClient.SignMessage(ctx, &signrpc.SignMessageReq{
		Msg:        payload.Bytes(),
		KeyLoc:     nodeKeyLocator,
		SchnorrSig: true,
	})
	if err != nil {
		if status.Code(err) == codes.PermissionDenied {
			return nil, fmt.Errorf("flnd refused signrpc SignMessage: the macaroon needs signer:generate: %w", err)
		}
		return nil, fmt.Errorf("flnd signrpc SignMessage: %w", err)
	}
	if len(resp.Signature) != 64 {
		return nil, fmt.Errorf("flnd signrpc SignMessage returned a %d-byte signature, want 64 (BIP-340)", len(resp.Signature))
	}
	return resp.Signature, nil
}
