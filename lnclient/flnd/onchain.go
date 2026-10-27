package flnd

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/flokiorg/flnd/lnrpc"
	"github.com/flokiorg/flnd/lnrpc/chainrpc"
	"github.com/flokiorg/flnd/lnrpc/walletrpc"
	"github.com/flokiorg/go-flokicoin/chaincfg"
	"github.com/flokiorg/go-flokicoin/chaincfg/chainhash"
	"github.com/flokiorg/go-flokicoin/chainutil"
	"github.com/flokiorg/go-flokicoin/chainutil/psbt"
	"github.com/flokiorg/go-flokicoin/txscript"
	"github.com/flokiorg/go-flokicoin/wire"

	"github.com/flokiorg/lokihub/lnclient"
)

// onchainTimeout bounds every on-chain call; the payout engine retries on its
// next pass.
const onchainTimeout = 20 * time.Second

var _ lnclient.OnchainPayer = (*FLNDService)(nil)

func bounded(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, onchainTimeout)
}

func outpointOf(s string) (*lnrpc.OutPoint, error) {
	i := strings.LastIndexByte(s, ':')
	if i < 0 {
		return nil, fmt.Errorf("outpoint %q", s)
	}
	n, err := strconv.ParseUint(s[i+1:], 10, 32)
	if err != nil {
		return nil, err
	}
	return &lnrpc.OutPoint{TxidStr: s[:i], OutputIndex: uint32(n)}, nil
}

func outpointString(o *lnrpc.OutPoint) string {
	if o == nil {
		return ""
	}
	txid := o.TxidStr
	if txid == "" && len(o.TxidBytes) == 32 {
		if h, err := chainhash.NewHash(o.TxidBytes); err == nil {
			txid = h.String()
		}
	}
	return fmt.Sprintf("%s:%d", txid, o.OutputIndex)
}

func (svc *FLNDService) ListUnspent(ctx context.Context, minConfs int32, account string) ([]lnclient.Utxo, error) {
	ctx, cancel := bounded(ctx)
	defer cancel()
	resp, err := svc.client.WalletKit().ListUnspent(ctx, &walletrpc.ListUnspentRequest{MinConfs: minConfs, MaxConfs: 1 << 30, Account: account})
	if err != nil {
		return nil, err
	}
	out := make([]lnclient.Utxo, 0, len(resp.Utxos))
	listed := map[string]bool{}
	for _, u := range resp.Utxos {
		script, err := hex.DecodeString(u.PkScript)
		if err != nil {
			return nil, err
		}
		op := outpointString(u.Outpoint)
		listed[op] = true
		out = append(out, lnclient.Utxo{Outpoint: op, ValueSat: u.AmountSat, Script: script,
			Confs: u.Confirmations, Account: account})
	}
	if account != "" && account != "default" {
		return out, nil
	}
	// The wallet leaves leased coins out of ListUnspent, but they are still the
	// wallet's: the payout engine leases the coins it sets aside and must keep
	// seeing them (and their callers decide what a foreign lease means).
	leased, err := svc.leasedUnspent(ctx, minConfs, listed)
	if err != nil {
		return nil, err
	}
	return append(out, leased...), nil
}

// leasedUnspent lists the wallet's leased, unspent coins with at least
// minConfs confirmations that are not in listed.
func (svc *FLNDService) leasedUnspent(ctx context.Context, minConfs int32, listed map[string]bool) ([]lnclient.Utxo, error) {
	leases, err := svc.client.WalletKit().ListLeases(ctx, &walletrpc.ListLeasesRequest{})
	if err != nil {
		return nil, err
	}
	var pending []*walletrpc.UtxoLease
	for _, l := range leases.LockedUtxos {
		if !listed[outpointString(l.Outpoint)] {
			pending = append(pending, l)
		}
	}
	if len(pending) == 0 {
		return nil, nil
	}
	confs := map[string]int64{}
	txs, err := svc.client.GetTransactions(ctx, &lnrpc.GetTransactionsRequest{StartHeight: 0, EndHeight: -1})
	if err != nil {
		return nil, err
	}
	for _, tx := range txs.Transactions {
		confs[tx.TxHash] = int64(tx.NumConfirmations)
	}
	var out []lnclient.Utxo
	for _, l := range pending {
		op := outpointString(l.Outpoint)
		txid := op[:strings.LastIndexByte(op, ':')]
		c, known := confs[txid]
		if !known || c < int64(minConfs) {
			continue
		}
		out = append(out, lnclient.Utxo{Outpoint: op, ValueSat: int64(l.Value), Script: l.PkScript, //nolint:gosec // an output value
			Confs: c, Account: "default"})
	}
	return out, nil
}

func (svc *FLNDService) LeaseOutput(ctx context.Context, id [32]byte, outpoint string, secs int64) (int64, error) {
	ctx, cancel := bounded(ctx)
	defer cancel()
	op, err := outpointOf(outpoint)
	if err != nil {
		return 0, err
	}
	resp, err := svc.client.WalletKit().LeaseOutput(ctx, &walletrpc.LeaseOutputRequest{Id: id[:], Outpoint: op, ExpirationSeconds: uint64(max(secs, 0))}) //nolint:gosec // non-negative
	if err != nil {
		return 0, err
	}
	return int64(resp.Expiration), nil //nolint:gosec // a unix time
}

func (svc *FLNDService) ReleaseOutput(ctx context.Context, id [32]byte, outpoint string) error {
	ctx, cancel := bounded(ctx)
	defer cancel()
	op, err := outpointOf(outpoint)
	if err != nil {
		return err
	}
	_, err = svc.client.WalletKit().ReleaseOutput(ctx, &walletrpc.ReleaseOutputRequest{Id: id[:], Outpoint: op})
	return err
}

func (svc *FLNDService) ListLeases(ctx context.Context) ([]lnclient.Lease, error) {
	ctx, cancel := bounded(ctx)
	defer cancel()
	resp, err := svc.client.WalletKit().ListLeases(ctx, &walletrpc.ListLeasesRequest{})
	if err != nil {
		return nil, err
	}
	out := make([]lnclient.Lease, 0, len(resp.LockedUtxos))
	for _, l := range resp.LockedUtxos {
		var id [32]byte
		copy(id[:], l.Id)
		out = append(out, lnclient.Lease{Outpoint: outpointString(l.Outpoint), LeaseID: id, Expiry: int64(l.Expiration)}) //nolint:gosec // a unix time
	}
	return out, nil
}

// FundPsbtFixed decorates every input of the template with its wallet output
// (the signer needs it) and leases it under id; it selects no coin and adds no
// change, so the transaction stays exactly as built.
func (svc *FLNDService) FundPsbtFixed(ctx context.Context, raw []byte, id [32]byte, lockSecs int64) ([]byte, error) {
	p, err := psbt.NewFromRawBytes(bytes.NewReader(raw), false)
	if err != nil {
		return nil, err
	}
	utxos, err := svc.ListUnspent(ctx, 0, "")
	if err != nil {
		return nil, err
	}
	byOutpoint := map[string]lnclient.Utxo{}
	for _, u := range utxos {
		byOutpoint[u.Outpoint] = u
	}
	for i, in := range p.UnsignedTx.TxIn {
		o := in.PreviousOutPoint.String()
		u, ok := byOutpoint[o]
		if !ok {
			return nil, fmt.Errorf("input %s is not an unspent wallet coin", o)
		}
		if _, err := svc.LeaseOutput(ctx, id, o, lockSecs); err != nil {
			return nil, fmt.Errorf("lease input %s: %w", o, err)
		}
		p.Inputs[i].WitnessUtxo = wire.NewTxOut(u.ValueSat, u.Script)
		p.Inputs[i].SighashType = sighashFor(u.Script)
	}
	var buf bytes.Buffer
	if err := p.Serialize(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (svc *FLNDService) FinalizePsbt(ctx context.Context, funded []byte) ([]byte, error) {
	ctx, cancel := bounded(ctx)
	defer cancel()
	resp, err := svc.client.WalletKit().FinalizePsbt(ctx, &walletrpc.FinalizePsbtRequest{FundedPsbt: funded})
	if err != nil {
		return nil, err
	}
	return resp.RawFinalTx, nil
}

// classifyPublish maps a node's refusal to lnclient's classes.
func classifyPublish(msg string) error {
	m := strings.ToLower(msg)
	switch {
	case m == "", strings.Contains(m, "already have"), strings.Contains(m, "already in mempool"),
		strings.Contains(m, "already known"), strings.Contains(m, "txn-already"):
		return nil
	case strings.Contains(m, "fee"), strings.Contains(m, "replacement"), strings.Contains(m, "rejecting replacement"):
		return fmt.Errorf("%w: %s", lnclient.ErrPublishFeeTooLow, msg)
	case strings.Contains(m, "missing"), strings.Contains(m, "spent"), strings.Contains(m, "orphan"):
		return fmt.Errorf("%w: %s", lnclient.ErrPublishInputGone, msg)
	}
	return errors.New(msg)
}

func (svc *FLNDService) PublishTx(ctx context.Context, raw []byte) error {
	ctx, cancel := bounded(ctx)
	defer cancel()
	resp, err := svc.client.WalletKit().PublishTransaction(ctx, &walletrpc.Transaction{TxHex: raw, Label: "cash_withdraw"})
	if err != nil {
		return classifyPublish(err.Error())
	}
	return classifyPublish(resp.PublishError)
}

func (svc *FLNDService) SubmitPackage(ctx context.Context, parent, child []byte) error {
	ctx, cancel := bounded(ctx)
	defer cancel()
	resp, err := svc.client.WalletKit().SubmitPackage(ctx, &walletrpc.SubmitPackageRequest{RawTxs: [][]byte{parent, child}})
	if err != nil {
		return classifyPublish(err.Error())
	}
	if resp.PackageMsg != "" && !strings.EqualFold(resp.PackageMsg, "success") {
		return classifyPublish(resp.PackageMsg)
	}
	return nil
}

func (svc *FLNDService) EstimateFee(ctx context.Context, conf int) (lnclient.FeeEstimate, error) {
	ctx, cancel := bounded(ctx)
	defer cancel()
	resp, err := svc.client.WalletKit().EstimateFee(ctx, &walletrpc.EstimateFeeRequest{ConfTarget: int32(max(conf, 2))}) //nolint:gosec // small
	if err != nil {
		return lnclient.FeeEstimate{}, err
	}
	return lnclient.FeeEstimate{SatPerKw: resp.SatPerKw, MinRelaySatPerKw: resp.MinRelayFeeSatPerKw}, nil
}

func (svc *FLNDService) RequiredReserve(ctx context.Context) (int64, error) {
	ctx, cancel := bounded(ctx)
	defer cancel()
	resp, err := svc.client.WalletKit().RequiredReserve(ctx, &walletrpc.RequiredReserveRequest{})
	if err != nil {
		return 0, err
	}
	return resp.RequiredReserve, nil
}

func (svc *FLNDService) chainParams() *chaincfg.Params {
	switch svc.nodeInfo.Network {
	case "testnet", "testnet3", "test":
		return &chaincfg.TestNet3Params
	case "regtest":
		return &chaincfg.RegressionNetParams
	case "simnet":
		return &chaincfg.SimNetParams
	}
	return &chaincfg.MainNetParams
}

func (svc *FLNDService) ListWalletScripts(ctx context.Context) ([][]byte, error) {
	ctx, cancel := bounded(ctx)
	defer cancel()
	resp, err := svc.client.WalletKit().ListAddresses(ctx, &walletrpc.ListAddressesRequest{ShowCustomAccounts: true})
	if err != nil {
		return nil, err
	}
	params := svc.chainParams()
	var out [][]byte
	for _, acct := range resp.AccountWithAddresses {
		for _, a := range acct.Addresses {
			addr, err := chainutil.DecodeAddress(a.Address, params)
			if err != nil {
				continue
			}
			script, err := txscript.PayToAddrScript(addr)
			if err != nil {
				continue
			}
			out = append(out, script)
		}
	}
	return out, nil
}

func (svc *FLNDService) NextChangeScript(ctx context.Context) ([]byte, error) {
	ctx, cancel := bounded(ctx)
	defer cancel()
	resp, err := svc.client.WalletKit().NextAddr(ctx, &walletrpc.AddrRequest{Type: walletrpc.AddressType_WITNESS_PUBKEY_HASH, Change: true})
	if err != nil {
		return nil, err
	}
	addr, err := chainutil.DecodeAddress(resp.Addr, svc.chainParams())
	if err != nil {
		return nil, err
	}
	return txscript.PayToAddrScript(addr)
}

func (svc *FLNDService) WalletTx(ctx context.Context, txid string) (lnclient.WalletTx, error) {
	ctx, cancel := bounded(ctx)
	defer cancel()
	tx, err := svc.client.WalletKit().GetTransaction(ctx, &walletrpc.GetTransactionRequest{Txid: txid})
	if err != nil {
		if txUnknown(err) {
			return lnclient.WalletTx{}, nil
		}
		return lnclient.WalletTx{}, err
	}
	return lnclient.WalletTx{Found: true, Confs: int64(tx.NumConfirmations), BlockHash: tx.BlockHash, BlockHeight: int64(tx.BlockHeight)}, nil
}

func (svc *FLNDService) ConfirmedBalance(ctx context.Context) (int64, error) {
	ctx, cancel := bounded(ctx)
	defer cancel()
	resp, err := svc.client.Lightning().WalletBalance(ctx, &lnrpc.WalletBalanceRequest{})
	if err != nil {
		return 0, err
	}
	// Leased coins (those the payout engine set aside) are confirmed coins of
	// the wallet too; WalletBalance counts them apart.
	return resp.ConfirmedBalance + resp.LockedBalance, nil
}

func (svc *FLNDService) BestBlock(ctx context.Context) (lnclient.BlockEpoch, error) {
	ctx, cancel := bounded(ctx)
	defer cancel()
	resp, err := svc.client.ChainKit().GetBestBlock(ctx, &chainrpc.GetBestBlockRequest{})
	if err != nil {
		return lnclient.BlockEpoch{}, err
	}
	h, err := chainhash.NewHash(resp.BlockHash)
	if err != nil {
		return lnclient.BlockEpoch{}, err
	}
	return lnclient.BlockEpoch{Height: int64(resp.BlockHeight), Hash: h.String()}, nil
}

func (svc *FLNDService) SubscribeBlocks(ctx context.Context) (<-chan lnclient.BlockEpoch, error) {
	stream, err := svc.client.ChainNotifier().RegisterBlockEpochNtfn(ctx, &chainrpc.BlockEpoch{})
	if err != nil {
		return nil, err
	}
	out := make(chan lnclient.BlockEpoch, 4)
	go func() {
		defer close(out)
		for {
			ev, err := stream.Recv()
			if err != nil {
				return
			}
			h, err := chainhash.NewHash(ev.Hash)
			if err != nil {
				continue
			}
			select {
			case out <- lnclient.BlockEpoch{Height: int64(ev.Height), Hash: h.String()}:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

// txUnknown reports the wallet's answer for a transaction it does not hold
// (flnd says "can not find transaction"; other versions "not found"): a
// version signed but never published, which is no error.
func txUnknown(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "not found") || strings.Contains(msg, "can not find") ||
		strings.Contains(msg, "cannot find")
}

// sighashFor is the sighash type an input of a payout version is signed with:
// SIGHASH_ALL, written out for a segwit v0 coin (flnd signs a PSBT input with
// no type as type 0, which a v0 signature may not carry: lokid refused every
// such version "invalid hash type 0x0", live run23); SIGHASH_DEFAULT (0) for
// a taproot coin.
func sighashFor(script []byte) txscript.SigHashType {
	if txscript.IsPayToTaproot(script) {
		return txscript.SigHashDefault
	}
	return txscript.SigHashAll
}
