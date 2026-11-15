// Package nodesign signs with the node identity key: bills (NIP-CASH §Mint
// Provenance) and connection strings (§Connection Signatures) go through the
// node's signed-message convention, which flnd applies itself.
package nodesign

import (
	"context"
	"fmt"
	"time"

	"github.com/ohstr/nmilat/nipcash"
	"github.com/tv42/zbase32"

	"github.com/flokiorg/lokihub/lnclient"
)

// Timeout bounds one node signature, so a stuck node fails the call before
// anything is committed.
const Timeout = 3 * time.Second

// Chain is the chain name this Hub signs connection strings under and reports in
// get_info (NIP-CASH §Network and Chain).
const Chain = "flokicoin"

// Signer signs a payload with the node identity key and returns the 65-byte
// compact recoverable signature nipcash expects.
func Signer(ctx context.Context, ln lnclient.LNClient) nipcash.MintSigner {
	return func(payload string) ([]byte, error) {
		if err := Guard.Allow(ctx); err != nil {
			return nil, err
		}
		sctx, cancel := context.WithTimeout(ctx, Timeout)
		defer cancel()
		zsig, err := ln.SignMessage(sctx, payload)
		if err != nil {
			return nil, fmt.Errorf("the node refused to sign: %w", err)
		}
		raw, err := zbase32.DecodeString(zsig)
		if err != nil {
			return nil, fmt.Errorf("the node's signature does not decode: %w", err)
		}
		return raw, nil
	}
}

// XOnly is the node identity key, x-only, as 64 lowercase hex.
func XOnly(ctx context.Context, ln lnclient.LNClient) (string, error) {
	if ln == nil {
		return "", fmt.Errorf("the node is not started")
	}
	info, err := ln.GetInfo(ctx)
	if err != nil {
		return "", fmt.Errorf("could not read the node identity: %w", err)
	}
	return nipcash.MinterXOnly(info.Pubkey)
}
