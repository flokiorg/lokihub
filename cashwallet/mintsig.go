package cashwallet

import (
	"context"

	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/logger"
	"github.com/flokiorg/lokihub/lokicash"
	"github.com/tv42/zbase32"
)

// signMintProvenance produces the raw recoverable signature a lokicash token
// carries as its optional mint-provenance (NIP-CASH §Mint Provenance): the node
// signs lokicash.MintPayload(HRP, walletPubkey, amountMillis) with its Lightning
// identity key, and we return the raw compact bytes (LND hands back a zbase32
// string, which we decode so the token stays compact).
//
// Provenance is best-effort: if signing or decoding fails, this returns
// ok=false and the caller MUST mint the token without provenance rather than
// fail the whole mint — the token is fully spendable either way, and a missing
// signature only costs the offline origin/denomination proof.
func signMintProvenance(ctx context.Context, ln lnclient.LNClient, walletPubkey string, amountMillis uint64) (sig []byte, ok bool) {
	payload := lokicash.MintPayload(lokicash.HRP, walletPubkey, amountMillis)
	zsig, err := ln.SignMessage(ctx, payload)
	if err != nil {
		logger.Logger.Warn().Err(err).Str("wallet_pubkey", walletPubkey).
			Msg("Failed to sign cash mint provenance; minting token without it")
		return nil, false
	}
	raw, err := zbase32.DecodeString(zsig)
	if err != nil {
		logger.Logger.Warn().Err(err).Str("wallet_pubkey", walletPubkey).
			Msg("Failed to decode mint provenance signature; minting token without it")
		return nil, false
	}
	// A well-formed LND SignMessage result is a 65-byte compact recoverable
	// signature; anything else can't be a valid provenance signature, so drop it.
	if len(raw) != mintSigRawLen {
		logger.Logger.Warn().Int("len", len(raw)).Str("wallet_pubkey", walletPubkey).
			Msg("Unexpected mint provenance signature length; minting token without it")
		return nil, false
	}
	return raw, true
}

// mintSigRawLen is the byte length of a recoverable ECDSA compact signature,
// mirrored from lokicash so this package can validate before handing bytes off.
const mintSigRawLen = 65

// encodeCashToken builds a lokicash token for a freshly-created wallet,
// attaching mint provenance when signMint is set and signing succeeds. It never
// returns an error: a token is best-effort metadata layered over PairingURI
// (which is always sufficient on its own), so any encode/sign failure degrades
// to the plainest token that still works, with the failure logged. amountMillis
// is the wallet's total committed amount (the value the signature attests).
func encodeCashToken(ctx context.Context, ln lnclient.LNClient, walletPubkey, secret string, relayURLs []string, identityRequired *bool, signMint bool, amountMillis uint64) string {
	tok := lokicash.Token{
		HRP:              lokicash.HRP,
		WalletPubkey:     walletPubkey,
		Secret:           secret,
		RelayURLs:        relayURLs,
		IdentityRequired: identityRequired,
	}
	if signMint {
		if sig, ok := signMintProvenance(ctx, ln, walletPubkey, amountMillis); ok {
			amt := amountMillis
			tok.MintSignature = sig
			tok.AttestedAmount = &amt
		}
	}
	token, err := lokicash.Encode(tok)
	if err != nil {
		logger.Logger.Error().Err(err).Str("wallet_pubkey", walletPubkey).
			Msg("Failed to encode lokicash token for already-funded Cash wallet")
		return ""
	}
	return token
}
