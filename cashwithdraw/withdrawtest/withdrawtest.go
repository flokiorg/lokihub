// Package withdrawtest holds test doubles for the on-chain withdrawal service:
// a node with a chain tip and the node identity key, fee estimates and wallet
// scripts, and a payout engine whose set-aside rolls back with the commit.
// Tests only.
package withdrawtest

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/flokiorg/go-flokicoin/chaincfg"
	"github.com/nbd-wtf/go-nostr"
	"github.com/ohstr/nmilat/nip01"
	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/cashwithdraw"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/lnclient"
)

// Node is a regtest node: a tip, the identity key (hex), an optional signing
// failure and an optional on-chain capability.
type Node struct {
	mu      sync.Mutex
	Height  int64
	PrivHex string
	SignErr error
	OnChain lnclient.OnchainPayer
}

func (n *Node) Tip(context.Context) (int64, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.Height, nil
}
func (n *Node) SetTip(h int64) { n.mu.Lock(); n.Height = h; n.mu.Unlock() }
func (n *Node) HRP(context.Context) (string, error) {
	return chaincfg.RegressionNetParams.Bech32HRPSegwit, nil
}
func (n *Node) XOnly(context.Context) (string, error) { return nostr.GetPublicKey(n.PrivHex) }
func (n *Node) SignEvent(_ context.Context, ev *nip01.Event) error {
	n.mu.Lock()
	err := n.SignErr
	n.mu.Unlock()
	if err != nil {
		return err
	}
	return ev.Sign(n.PrivHex)
}
func (n *Node) Payer() lnclient.OnchainPayer { return n.OnChain }

// ErrUnused is what the payer answers for calls a WP-23 test never makes.
var ErrUnused = errors.New("withdrawtest: not used")

// Payer answers fee estimates and the wallet's scripts.
type Payer struct {
	mu      sync.Mutex
	Est     lnclient.FeeEstimate
	Scripts [][]byte
}

func (p *Payer) ListUnspent(context.Context, int32, string) ([]lnclient.Utxo, error) {
	return nil, ErrUnused
}
func (p *Payer) LeaseOutput(context.Context, [32]byte, string, int64) (int64, error) {
	return 0, ErrUnused
}
func (p *Payer) ReleaseOutput(context.Context, [32]byte, string) error { return ErrUnused }
func (p *Payer) ListLeases(context.Context) ([]lnclient.Lease, error)  { return nil, ErrUnused }
func (p *Payer) FundPsbtFixed(context.Context, []byte, [32]byte, int64) ([]byte, error) {
	return nil, ErrUnused
}
func (p *Payer) FinalizePsbt(context.Context, []byte) ([]byte, error) { return nil, ErrUnused }
func (p *Payer) PublishTx(context.Context, []byte) error              { return ErrUnused }
func (p *Payer) SubmitPackage(context.Context, []byte, []byte) error  { return ErrUnused }
func (p *Payer) EstimateFee(context.Context, int) (lnclient.FeeEstimate, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.Est, nil
}
func (p *Payer) RequiredReserve(context.Context) (int64, error) { return 0, ErrUnused }
func (p *Payer) ListWalletScripts(context.Context) ([][]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([][]byte(nil), p.Scripts...), nil
}
func (p *Payer) NextChangeScript(context.Context) ([]byte, error) { return nil, ErrUnused }
func (p *Payer) WalletTx(context.Context, string) (lnclient.WalletTx, error) {
	return lnclient.WalletTx{}, ErrUnused
}
func (p *Payer) ConfirmedBalance(context.Context) (int64, error) { return 0, ErrUnused }
func (p *Payer) BestBlock(context.Context) (lnclient.BlockEpoch, error) {
	return lnclient.BlockEpoch{}, ErrUnused
}
func (p *Payer) SubscribeBlocks(context.Context) (<-chan lnclient.BlockEpoch, error) {
	return nil, ErrUnused
}

// Funds is a ready payout engine with switches for the bound and the coins; its
// set-aside is a user_configs row written in the commit's transaction.
type Funds struct {
	mu       sync.Mutex
	NotReady bool
	AtBound  bool
	NoCoins  bool
	Signals  int
}

// SetAsidePrefix prefixes the user_configs key of each set-aside.
const SetAsidePrefix = "wdtest-setaside-"

func (f *Funds) Ready() bool { f.mu.Lock(); defer f.mu.Unlock(); return !f.NotReady }
func (f *Funds) Snapshot(context.Context) (cashwithdraw.FundsSnapshot, error) {
	return cashwithdraw.FundsSnapshot{ConfirmedSat: 1e9}, nil
}
func (f *Funds) AtUnconfirmedBoundTx(*gorm.DB, cashwithdraw.FundsSnapshot, uint64) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.AtBound, nil
}
func (f *Funds) CanSetAsideTx(*gorm.DB, uint64, uint64) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return !f.NoCoins, nil
}
func (f *Funds) SetAsideTx(tx *gorm.DB, id uint, _, _ uint64) error {
	return tx.Create(&db.UserConfig{Key: fmt.Sprintf("%s%d", SetAsidePrefix, id), Value: "1"}).Error
}
func (f *Funds) Signal() { f.mu.Lock(); f.Signals++; f.mu.Unlock() }
