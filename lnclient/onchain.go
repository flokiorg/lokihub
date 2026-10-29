package lnclient

import (
	"context"
	"errors"
)

// OnchainPayer is the node capability on-chain withdrawals need (NIP-CASH §Paying
// a Payout): the wallet's coins and their leases, a PSBT funded on exactly the
// inputs and outputs it is given and signed without being published, publishing
// with classified errors, fee estimates, the wallet's own scripts and a chain
// view. Like TransportSigner it is optional and type-asserted: a backend without
// it cannot enable withdrawals. Every call is bounded by its context.
//
// Outpoints are "txid:vout" with the txid in its usual (reversed, lowercase hex)
// form; scripts are raw output scripts.
type OnchainPayer interface {
	// ListUnspent lists the wallet's coins with at least minConfs
	// confirmations in account ("" is the default account).
	ListUnspent(ctx context.Context, minConfs int32, account string) ([]Utxo, error)
	// LeaseOutput leases outpoint under leaseID (32 bytes) for secs seconds, so
	// the node's own spends skip it, and returns the lease's expiry (unix).
	LeaseOutput(ctx context.Context, leaseID [32]byte, outpoint string, secs int64) (int64, error)
	// ReleaseOutput releases a lease taken under leaseID.
	ReleaseOutput(ctx context.Context, leaseID [32]byte, outpoint string) error
	// ListLeases lists every lease the wallet holds.
	ListLeases(ctx context.Context) ([]Lease, error)
	// FundPsbtFixed locks and decorates the inputs of a template PSBT that
	// already holds every input and output: no coin selection, no change added,
	// no signature. It returns the funded PSBT.
	FundPsbtFixed(ctx context.Context, psbt []byte, leaseID [32]byte, lockSecs int64) ([]byte, error)
	// FinalizePsbt signs a funded PSBT and returns the raw transaction. It never
	// publishes it.
	FinalizePsbt(ctx context.Context, psbt []byte) ([]byte, error)
	// PublishTx publishes a raw transaction. nil means accepted or already
	// known; the errors below classify a refusal.
	PublishTx(ctx context.Context, raw []byte) error
	// SubmitPackage publishes a parent with a child paying for both.
	SubmitPackage(ctx context.Context, parent, child []byte) error
	// EstimateFee is the node's fee estimate for confTarget blocks and its
	// minimum relay fee, both in base units per 1000 weight units.
	EstimateFee(ctx context.Context, confTarget int) (FeeEstimate, error)
	// RequiredReserve is what the node keeps apart for its channels, in base
	// units.
	RequiredReserve(ctx context.Context) (int64, error)
	// ListWalletScripts lists the output script of every address of every
	// account of the wallet, change included.
	ListWalletScripts(ctx context.Context) ([][]byte, error)
	// NextChangeScript is a fresh change output script of the default account.
	NextChangeScript(ctx context.Context) ([]byte, error)
	// WalletTx is what the wallet knows of a transaction: its confirmations
	// and, when confirmed, its block. Found is false for an unknown txid.
	WalletTx(ctx context.Context, txid string) (WalletTx, error)
	// ConfirmedBalance is the wallet's confirmed balance, in base units.
	ConfirmedBalance(ctx context.Context) (int64, error)
	// SpendableBalance is what the wallet's own coin selection may spend: its
	// confirmed coins under no lease, in base units.
	SpendableBalance(ctx context.Context) (int64, error)
	// BestBlock is the node's chain tip.
	BestBlock(ctx context.Context) (BlockEpoch, error)
	// SubscribeBlocks delivers each new tip until ctx ends.
	SubscribeBlocks(ctx context.Context) (<-chan BlockEpoch, error)
}

// Utxo is one wallet coin.
type Utxo struct {
	Outpoint string
	ValueSat int64
	Script   []byte
	Confs    int64
	Account  string
}

// Lease is one leased wallet coin.
type Lease struct {
	Outpoint string
	LeaseID  [32]byte
	Expiry   int64
}

// FeeEstimate is a fee rate and the minimum relay rate, in base units per 1000
// weight units (sat/kw).
type FeeEstimate struct {
	SatPerKw         int64
	MinRelaySatPerKw int64
}

// WalletTx is a transaction as the wallet sees it.
type WalletTx struct {
	Found       bool
	Confs       int64
	BlockHash   string
	BlockHeight int64
}

// BlockEpoch is a chain tip.
type BlockEpoch struct {
	Height int64
	Hash   string
}

// PublishTx's classified refusals.
var (
	// ErrPublishFeeTooLow: the network's nodes do not relay it at this fee, or
	// it does not pay enough to replace what it conflicts with.
	ErrPublishFeeTooLow = errors.New("transaction fee too low to relay or replace")
	// ErrPublishInputGone: an input is missing or already spent.
	ErrPublishInputGone = errors.New("transaction input missing or spent")
)
