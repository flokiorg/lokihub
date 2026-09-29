package cashwallet

import (
	"context"
	"fmt"

	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/logger"
	"github.com/flokiorg/lokihub/lokicash"
	"github.com/tv42/zbase32"
)

// MintProvenance produces the raw recoverable signature a lokicash token carries as
// its mint provenance (NIP-CASH §Mint Provenance): the node signs
// lokicash.MintPayload(HRP, walletPubkey, amountMillis) with its Lightning identity
// key, and we return the raw compact bytes (LND hands back a zbase32 string, which we
// decode so the token stays compact).
//
// Provenance is MANDATORY, and this returns an error rather than degrading. An
// unsigned bill carries nothing that identifies its minting hub, and that identity is
// the only thing a client can verify a transport announcement against — so an
// unsigned bill can never reach the private transport, which is the only transport
// that serves bill methods. A bill nobody can spend is worse than a mint that refused.
//
// MUST be called BEFORE any funds move. That is the whole reason this is separate from
// encodeCashToken: signing is fallible and touches the node, encoding is pure. Called
// after funding, a failure would have to be swallowed — the money would already be
// gone — which is exactly how provenance ended up optional in the first place.
func MintProvenance(ctx context.Context, ln lnclient.LNClient, walletPubkey string, amountMillis uint64) ([]byte, error) {
	payload := lokicash.MintPayload(lokicash.HRP, walletPubkey, amountMillis)
	zsig, err := ln.SignMessage(ctx, payload)
	if err != nil {
		return nil, fmt.Errorf("node refused to sign mint provenance for %s: %w", walletPubkey, err)
	}
	raw, err := zbase32.DecodeString(zsig)
	if err != nil {
		return nil, fmt.Errorf("could not decode the node's mint provenance signature for %s: %w", walletPubkey, err)
	}
	// A well-formed LND SignMessage result is a 65-byte compact recoverable signature;
	// anything else cannot be a valid provenance signature.
	if len(raw) != mintSigRawLen {
		return nil, fmt.Errorf("mint provenance signature for %s is %d bytes, want %d", walletPubkey, len(raw), mintSigRawLen)
	}
	return raw, nil
}

// mintSigRawLen is the byte length of a recoverable ECDSA compact signature,
// mirrored from lokicash so this package can validate before handing bytes off.
const mintSigRawLen = 65

// encodeCashToken builds a lokicash token for a freshly-created wallet, attaching the
// mint provenance already obtained by MintProvenance.
//
// Pure and infallible-by-contract: it never returns an error because a token is
// metadata layered over PairingURI (always sufficient on its own), and by this point
// funds have moved, so an error return would tell the caller creation failed when it
// succeeded. Everything that CAN genuinely fail — the node signature — happened before
// the money did.
//
// amountMillis is the wallet's total committed amount, the value mintSig attests.
func encodeCashToken(walletPubkey, secret string, relayURLs []string, identityRequired *bool, mintSig []byte, amountMillis uint64) string {
	amt := amountMillis
	tok := lokicash.Token{
		HRP:              lokicash.HRP,
		WalletPubkey:     walletPubkey,
		Secret:           secret,
		RelayURLs:        relayURLs,
		IdentityRequired: identityRequired,
		MintSignature:    mintSig,
		AttestedAmount:   &amt,
	}
	token, err := lokicash.Encode(tok)
	if err != nil {
		logger.Logger.Error().Err(err).Str("wallet_pubkey", walletPubkey).
			Msg("Failed to encode lokicash token for already-funded Cash wallet")
		return ""
	}
	return token
}
