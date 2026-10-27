package cashwallet

import (
	"context"
	"fmt"

	"github.com/ohstr/nmilat/nipcash"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/logger"
	"github.com/flokiorg/lokihub/nodesign"
)

// hubGroupInput is the key a Cash Hub's fingerprint is derived from (NIP-CASH §The
// Hub Group Fingerprint). It is the Hub's own connection pubkey, fixed once it mints:
// after the connection is regenerated, the key it had before.
func hubGroupInput(hub *db.App) string {
	if hub.HubGroupPubkey != nil && *hub.HubGroupPubkey != "" {
		return *hub.HubGroupPubkey
	}
	return hub.AppPubkey
}

// issueCashToken signs a new bill with the node identity, under cash-mint:v1, or
// under cash-zap:v1 when zap is set (a bill created by a zap transfer), and
// encodes it (NIP-CASH §Mint Provenance, §Wire Format), returning the signed token
// and its encoding. It runs inside the call's commit transaction, before COMMIT: a
// Hub never issues an unsigned bill, and a failure here rolls the whole call back.
func issueCashToken(ctx context.Context, ln lnclient.LNClient, walletPubkey, secret string, relayURLs []string, identityRequired *bool, amountMillis uint64, hub *db.App, zap *nipcash.ZapContext) (nipcash.Token, string, error) {
	amt := amountMillis
	tok := nipcash.Token{
		HRP:                  constants.CashTokenHRP,
		WalletPubkey:         walletPubkey,
		Secret:               secret,
		RelayURLs:            relayURLs,
		IdentityRequired:     identityRequired,
		AttestedAmountMillis: &amt,
	}
	// An empty input stamps no fingerprint, never a hash of "": that would group
	// bills across Hubs.
	if input := hubGroupInput(hub); input != "" {
		tok.HubGroup = nipcash.HubGroupFor(input)
	}
	xonly, err := nodesign.XOnly(ctx, ln)
	if err != nil {
		return nipcash.Token{}, "", err
	}
	scheme := nipcash.SchemeMintV1
	if zap != nil {
		scheme = nipcash.SchemeZapV1
	}
	signed, err := nipcash.SignToken(tok, scheme, zap, xonly, nodesign.Signer(ctx, ln))
	if err != nil {
		logger.Logger.Error().Err(err).Str("wallet_pubkey", walletPubkey).Msg("Could not sign a bill's mint provenance")
		return nipcash.Token{}, "", fmt.Errorf("provenance unavailable; nothing was committed: %w", err)
	}
	encoded, err := nipcash.Encode(signed)
	if err != nil {
		return nipcash.Token{}, "", err
	}
	return signed, encoded, nil
}
