package cashwallet

import (
	"context"
	"fmt"
	"time"

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

// Signing runs inside the call's commit transaction, before COMMIT: a bill's
// pairing key is derived from its app id, which only the open transaction
// assigns, and a bill must exist for no one until its signature does. Every row
// the commit wrote rolls back when a signature fails, so no unsigned or half
// signed bill is ever visible. What signing inside the transaction costs is lock
// time, and signPhase bounds it: one deadline for every signature of a commit,
// so a slow or stuck node holds the write lock for at most SignBudget(bills).
const (
	// signBudgetBase covers the node identity read and the first signature.
	signBudgetBase = nodesign.Timeout
	// signBudgetPerBill is each further bill's share; a healthy node signs in
	// about a millisecond.
	signBudgetPerBill = 25 * time.Millisecond
)

// SignBudget is the longest a commit creating bills bills may spend signing.
func SignBudget(bills int) time.Duration {
	return signBudgetBase + time.Duration(max(bills, 0))*signBudgetPerBill
}

// signPhase signs one commit's bills under one deadline, with the node identity
// read once.
type signPhase struct {
	ctx    context.Context
	cancel context.CancelFunc
	ln     lnclient.LNClient
	xonly  string
}

// newSignPhase starts the signing of bills bills; the caller must call done.
func newSignPhase(ctx context.Context, ln lnclient.LNClient, bills int) (*signPhase, error) {
	sctx, cancel := context.WithTimeout(ctx, SignBudget(bills))
	xonly, err := nodesign.XOnly(sctx, ln)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("provenance unavailable; nothing was committed: %w", err)
	}
	return &signPhase{ctx: sctx, cancel: cancel, ln: ln, xonly: xonly}, nil
}

func (p *signPhase) done() { p.cancel() }

// issue signs a new bill with the node identity, under cash-mint:v1, or under
// cash-zap:v1 when zap is set (a bill created by a zap transfer), and encodes it
// (NIP-CASH §Mint Provenance, §Wire Format), returning the signed token and its
// encoding. A failure, the phase's deadline included, rolls the whole call back.
func (p *signPhase) issue(walletPubkey, secret string, relayURLs []string, identityRequired *bool, amountMillis uint64, hub *db.App, zap *nipcash.ZapContext) (nipcash.Token, string, error) {
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
	if err := p.ctx.Err(); err != nil {
		return nipcash.Token{}, "", fmt.Errorf("provenance unavailable: the commit's signing budget ran out; nothing was committed: %w", err)
	}
	scheme := nipcash.SchemeMintV1
	if zap != nil {
		scheme = nipcash.SchemeZapV1
	}
	signed, err := nipcash.SignToken(tok, scheme, zap, p.xonly, nodesign.Signer(p.ctx, p.ln))
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
