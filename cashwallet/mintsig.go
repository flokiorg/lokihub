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
// Hub Group Fingerprint). It is the Hub's own connection pubkey, fixed once it mints.
func hubGroupInput(hub *db.App) string {
	return hub.AppPubkey
}

// issueCashToken signs a new bill under cash-mint:v1 with the node identity and
// encodes it (NIP-CASH §Mint Provenance, §Wire Format). It runs before any funds
// move: a Hub never issues an unsigned bill, and every failure here costs nothing.
func issueCashToken(ctx context.Context, ln lnclient.LNClient, walletPubkey, secret string, relayURLs []string, identityRequired *bool, amountMillis uint64, hub *db.App) (string, error) {
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
		return "", err
	}
	signed, err := nipcash.SignToken(tok, nipcash.SchemeMintV1, nil, xonly, nodesign.Signer(ctx, ln))
	if err != nil {
		logger.Logger.Error().Err(err).Str("wallet_pubkey", walletPubkey).Msg("Could not sign a bill's mint provenance")
		return "", fmt.Errorf("provenance unavailable; nothing was committed: %w", err)
	}
	return nipcash.Encode(signed)
}

// storeCashToken persists a freshly issued bill's token verbatim, so the archive
// keeps the exact string the bill was issued with once it is destroyed
// (db.App.CashToken). Best-effort: a bill that is otherwise fully minted and funded
// must not fail over its own audit copy.
func storeCashToken(deps Deps, walletAppID uint, token string) {
	if err := deps.DB.Model(&db.App{}).Where("id = ?", walletAppID).
		Update("cash_token", token).Error; err != nil {
		logger.Logger.Warn().Err(err).Uint("cash_wallet_id", walletAppID).
			Msg("Failed to store Cash bill token for the audit archive")
	}
}
