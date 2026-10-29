// Package cashwithdraw serves on-chain withdrawals (NIP-CASH §Redeeming
// On-chain): a Cash Hub's terms, quotes, the cash_withdraw commit that ends a
// slice and holds its amount, the payout's view for the reply, lookups and the
// cancel request, the re-mint of a cancelled payout, and the retention of its
// record. Paying the payout on-chain is the payout engine's (Funds).
package cashwithdraw

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/flokiorg/lokihub/hubclock"
	"sync"
	"sync/atomic"
	"time"

	"github.com/flokiorg/go-flokicoin/chaincfg"
	"github.com/ohstr/nmilat/nip01"
	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/cashwallet"
	"github.com/flokiorg/lokihub/config"
	"github.com/flokiorg/lokihub/events"
	"github.com/flokiorg/lokihub/keys"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/nodesign"
)

// Node is what the withdrawal code needs from the Lightning node: the chain
// tip, the network's witness HRP, the node identity and a signature by it over
// an event, and the optional on-chain capability (nil without it).
type Node interface {
	Tip(ctx context.Context) (int64, error)
	HRP(ctx context.Context) (string, error)
	XOnly(ctx context.Context) (string, error)
	SignEvent(ctx context.Context, ev *nip01.Event) error
	Payer() lnclient.OnchainPayer
}

// FundsSnapshot is what the payout engine reads from the node outside any
// database transaction, for the checks a commit repeats under its locks.
type FundsSnapshot struct {
	// ConfirmedSat is the wallet's confirmed on-chain balance.
	ConfirmedSat int64
}

// Funds is the payout engine (WP-24) as the commit sees it: whether it is
// ready (its startup reconcile done), the bound on unconfirmed payouts, the
// coins it can set aside, and the set-aside itself inside the commit. Signal
// wakes it after a commit or a cancel request.
type Funds interface {
	Ready() bool
	Snapshot(ctx context.Context) (FundsSnapshot, error)
	// AtUnconfirmedBoundTx: the unconfirmed payouts plus payoutMillis exceed
	// the bound.
	AtUnconfirmedBoundTx(tx *gorm.DB, snap FundsSnapshot, payoutMillis uint64) (bool, error)
	// CanSetAsideTx: coins can be set aside for a payout of payoutMillis whose
	// quoted network fee is networkFeeMillis.
	CanSetAsideTx(tx *gorm.DB, payoutMillis, networkFeeMillis uint64) (bool, error)
	// SetAsideTx sets coins aside for the committed withdrawal, durably.
	SetAsideTx(tx *gorm.DB, withdrawalID uint, payoutMillis, networkFeeMillis uint64) error
	Signal()
}

// Service serves on-chain withdrawals for every Cash Hub of this node.
type Service struct {
	DB     *gorm.DB
	Cfg    config.Config
	Keys   keys.Keys
	Apps   apps.AppsService
	Events events.EventPublisher
	// LN is the current Lightning client (nil while the node is down).
	LN func() lnclient.LNClient
	// NodeOverride replaces the node built from LN (tests).
	NodeOverride Node
	IA           cashwallet.IATrustChecker
	// Funds is the payout engine; nil means withdrawals cannot be offered.
	Funds Funds
	// Orders are the NIP-CO facts a withdrawal reads; nil reads as none.
	Orders OrderFacts
	// CancelClockOffset moves the withdraw-scoped clock (cancelNow), seconds.
	CancelClockOffset atomic.Int64
	// Now is the clock; nil is time.Now.
	Now func() time.Time
	// Hooks is the commit's fault-injection seam (tests only).
	Hooks *TestHooks

	feeMu    sync.Mutex
	feeCache feeCacheEntry
	ownMu    sync.Mutex
	ownCache ownCacheEntry
	quoteMu  sync.Mutex
	quotes   map[string][]int64
}

// proofNow is the clock a claim proof's freshness is held to: the wall clock,
// as cash_redeem and the private transport's item proofs use, never the test
// hooks' Hub clock offset (hubclock), which moves the Hub's protocol state
// (expiry, retention, payouts) but not the time a caller signs at.
func (s *Service) proofNow() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return hubclock.Now()
}

// cancelNow is the withdraw-scoped clock: the clock CancelDue,
// CancelRequestAnswer and RemintExpiry read, moved by CancelClockOffset (a
// regtest test hook only; 0 in production). Envelopes, proofs, lookups and
// quotes read the ordinary clock.
func (s *Service) cancelNow() time.Time {
	return s.now().Add(time.Duration(s.CancelClockOffset.Load()) * time.Second)
}

func (s *Service) orders() OrderFacts {
	if s.Orders != nil {
		return s.Orders
	}
	return DBOrders{}
}

func (s *Service) env() config.WithdrawEnv {
	if s.Cfg == nil || s.Cfg.GetEnv() == nil {
		return config.WithdrawEnv{}
	}
	return s.Cfg.GetEnv().WithdrawEnv
}

// node is the Node in force: NodeOverride, else one over the current LN client.
func (s *Service) node() (Node, error) {
	if s.NodeOverride != nil {
		return s.NodeOverride, nil
	}
	if s.LN == nil {
		return nil, errors.New("the node is not started")
	}
	ln := s.LN()
	if ln == nil {
		return nil, errors.New("the node is not started")
	}
	return lnNode{ln: ln}, nil
}

// payer is the node's on-chain capability, or nil.
func (s *Service) payer() lnclient.OnchainPayer {
	n, err := s.node()
	if err != nil {
		return nil
	}
	return n.Payer()
}

// signer adapts the node's event signature to nmilat's sign callback.
func signer(ctx context.Context, n Node) func(*nip01.Event) error {
	return func(ev *nip01.Event) error { return n.SignEvent(ctx, ev) }
}

// lnNode is Node over an LN client.
type lnNode struct{ ln lnclient.LNClient }

func (n lnNode) Tip(ctx context.Context) (int64, error) {
	// The chain's own best block when the node has the on-chain kit: the
	// node info lokihub keeps is refreshed only now and then, and a quote's
	// holds_until and its expiry must follow the chain (live run21).
	if p := n.Payer(); p != nil {
		if best, err := p.BestBlock(ctx); err == nil && best.Height > 0 {
			return best.Height, nil
		}
	}
	info, err := n.ln.GetInfo(ctx)
	if err != nil {
		return 0, err
	}
	return int64(info.BlockHeight), nil
}

func (n lnNode) HRP(ctx context.Context) (string, error) {
	info, err := n.ln.GetInfo(ctx)
	if err != nil {
		return "", err
	}
	return NetworkHRP(info.Network)
}

func (n lnNode) XOnly(ctx context.Context) (string, error) { return nodesign.XOnly(ctx, n.ln) }

// SignEvent signs ev with the node identity (BIP340 over the event's NIP-01
// serialisation, which the node hashes into its id), as the transport
// announcement is signed.
func (n lnNode) SignEvent(ctx context.Context, ev *nip01.Event) error {
	ts, ok := n.ln.(lnclient.TransportSigner)
	if !ok {
		return errors.New("this LN backend cannot sign with the node identity")
	}
	xonly, err := n.XOnly(ctx)
	if err != nil {
		return err
	}
	ev.PubKey = xonly
	payload, err := ev.Serialize()
	if err != nil {
		return err
	}
	sctx, cancel := context.WithTimeout(ctx, nodesign.Timeout)
	defer cancel()
	sig, err := ts.SignSchnorrNodeKey(sctx, payload)
	if err != nil {
		return fmt.Errorf("the node refused to sign: %w", err)
	}
	if err := ev.Rehash(); err != nil {
		return err
	}
	ev.Sig = hex.EncodeToString(sig)
	if err := ev.Verify(nip01.WithoutPowCheck()); err != nil {
		return fmt.Errorf("the node's signature does not verify: %w", err)
	}
	return nil
}

func (n lnNode) Payer() lnclient.OnchainPayer {
	p, _ := n.ln.(lnclient.OnchainPayer)
	return p
}

// NetworkHRP is the witness HRP of the node's network.
func NetworkHRP(network string) (string, error) {
	switch network {
	case "bitcoin", "mainnet", "flokicoin", "main":
		return chaincfg.MainNetParams.Bech32HRPSegwit, nil
	case "testnet", "testnet3", "test":
		return chaincfg.TestNet3Params.Bech32HRPSegwit, nil
	case "regtest":
		return chaincfg.RegressionNetParams.Bech32HRPSegwit, nil
	case "simnet":
		return chaincfg.SimNetParams.Bech32HRPSegwit, nil
	}
	return "", fmt.Errorf("no witness HRP for network %q", network)
}
