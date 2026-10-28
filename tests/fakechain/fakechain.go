// Package fakechain is a fake chain and node wallet for the payout engine's
// tests (WP-24): wallet coins, leases with expiry, PSBTs funded on fixed inputs
// and finalized without publishing, a mempool that applies replacement rules,
// blocks, withheld transactions, evictions and reorganisations. It implements
// lnclient.OnchainPayer. Tests only.
package fakechain

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/flokiorg/go-flokicoin/chainutil/psbt"
	"github.com/flokiorg/go-flokicoin/wire"

	"github.com/flokiorg/lokihub/lnclient"
)

// IncrementalSatPerVB is the incremental relay rate a replacement adds.
const IncrementalSatPerVB = 1

// ErrUnknownOutput is the wallet's refusal to lease an output it does not hold.
var ErrUnknownOutput = errors.New("unknown output")

type block struct {
	hash  string
	txids []string
}

type mempoolTx struct {
	tx    *wire.MsgTx
	fee   int64
	vsize int64
}

type lease struct {
	id     [32]byte
	expiry int64
}

// Chain is the fake chain and the node's wallet on it.
type Chain struct {
	mu sync.Mutex

	base    int64 // height of the block before blocks[0]
	blocks  []block
	mempool map[string]*mempoolTx
	order   []string // mempool insertion order
	txs     map[string]*wire.MsgTx
	outputs map[string]*wire.TxOut // every output ever created, by outpoint

	walletScripts map[string]bool
	changeN       int
	leases        map[string]lease

	// Estimate and MinRelay are in sat per 1000 weight units.
	Estimate int64
	MinRelay int64
	Reserve  int64

	withheld map[string]bool
	// Finalized counts the signatures asked for each txid.
	Finalized map[string]int
	// FinalizeCalls counts FinalizePsbt calls; OnFinalize runs before each.
	FinalizeCalls int
	OnFinalize    func()
	// FailFinalize fails the next n FinalizePsbt calls, as a node timeout or
	// a locked wallet would.
	FailFinalize int
	// PublishHook, when set, may refuse a publish with its error.
	PublishHook func(txid string) error
	// WalletTxErr, when set, is every WalletTx call's answer.
	WalletTxErr error

	now  func() time.Time
	subs []chan lnclient.BlockEpoch
	seq  int
}

// New is a chain at height 100 with an empty wallet.
func New() *Chain {
	return &Chain{base: 100, mempool: map[string]*mempoolTx{}, txs: map[string]*wire.MsgTx{},
		outputs: map[string]*wire.TxOut{}, walletScripts: map[string]bool{}, leases: map[string]lease{},
		Estimate: 2500, MinRelay: 253, Reserve: 10_000, withheld: map[string]bool{}, Finalized: map[string]int{},
		now: time.Now}
}

// SetNow sets the clock leases expire on.
func (c *Chain) SetNow(now func() time.Time) { c.mu.Lock(); c.now = now; c.mu.Unlock() }

func (c *Chain) height() int64 { return c.base + int64(len(c.blocks)) }

func op(h string, i uint32) string { return fmt.Sprintf("%s:%d", h, i) }

// WalletScript is a fresh script of the wallet.
func (c *Chain) WalletScript() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.walletScriptLocked()
}

func (c *Chain) walletScriptLocked() []byte {
	c.changeN++
	sum := sha256.Sum256([]byte(fmt.Sprintf("wallet-%d", c.changeN)))
	script := append([]byte{0x00, 0x14}, sum[:20]...)
	c.walletScripts[hex.EncodeToString(script)] = true
	return script
}

// Fund gives the wallet a coin of valueSat with confs confirmations (mining
// blocks as needed) and returns its outpoint.
func (c *Chain) Fund(valueSat int64, confs int) string { return c.fund(valueSat, confs) }

func (c *Chain) fund(valueSat int64, confs int) string {
	c.mu.Lock()
	c.seq++
	tx := wire.NewMsgTx(2)
	sum := sha256.Sum256([]byte(fmt.Sprintf("coinbase-%d", c.seq)))
	var h [32]byte
	copy(h[:], sum[:])
	tx.AddTxIn(&wire.TxIn{PreviousOutPoint: wire.OutPoint{Hash: h, Index: 0xffffffff}})
	tx.AddTxOut(wire.NewTxOut(valueSat, c.walletScriptLocked()))
	id := tx.TxHash().String()
	c.txs[id] = tx
	c.outputs[op(id, 0)] = tx.TxOut[0]
	c.mempool[id] = &mempoolTx{tx: tx}
	c.order = append(c.order, id)
	c.mu.Unlock()
	if confs > 0 {
		c.Mine(1)
		if confs > 1 {
			c.Mine(confs - 1)
		}
	}
	return op(id, 0)
}

// Pay creates an outside transaction paying script valueSat, unconfirmed.
func (c *Chain) Pay(script []byte, valueSat int64) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seq++
	tx := wire.NewMsgTx(2)
	sum := sha256.Sum256([]byte(fmt.Sprintf("outside-%d", c.seq)))
	var h [32]byte
	copy(h[:], sum[:])
	tx.AddTxIn(&wire.TxIn{PreviousOutPoint: wire.OutPoint{Hash: h, Index: 0xffffffff}})
	tx.AddTxOut(wire.NewTxOut(valueSat, script))
	id := tx.TxHash().String()
	c.txs[id] = tx
	c.outputs[op(id, 0)] = tx.TxOut[0]
	c.mempool[id] = &mempoolTx{tx: tx}
	c.order = append(c.order, id)
	return op(id, 0)
}

// spentLocked maps every outpoint spent in the chain or the mempool to its
// spender.
func (c *Chain) spentLocked() map[string]string {
	out := map[string]string{}
	add := func(id string) {
		tx := c.txs[id]
		for _, in := range tx.TxIn {
			if in.PreviousOutPoint.Index == 0xffffffff {
				continue
			}
			out[in.PreviousOutPoint.String()] = id
		}
	}
	for _, b := range c.blocks {
		for _, id := range b.txids {
			add(id)
		}
	}
	for _, id := range c.order {
		if _, ok := c.mempool[id]; ok {
			add(id)
		}
	}
	return out
}

// confHeightLocked is a confirmed txid's height, 0 when unconfirmed.
func (c *Chain) confHeightLocked(id string) int64 {
	for i, b := range c.blocks {
		for _, t := range b.txids {
			if t == id {
				return c.base + int64(i) + 1
			}
		}
	}
	return 0
}

// Mine mines n blocks, each taking every mempool transaction not withheld
// whose inputs are known.
func (c *Chain) Mine(n int) {
	c.mu.Lock()
	for range n {
		var txids []string
		inBlock := map[string]bool{}
		for _, id := range c.order {
			if _, ok := c.mempool[id]; !ok || c.withheld[id] || inBlock[id] {
				continue
			}
			txids = append(txids, id)
			inBlock[id] = true
		}
		for _, id := range txids {
			delete(c.mempool, id)
		}
		c.order = c.order[:0]
		for id := range c.mempool {
			c.order = append(c.order, id)
		}
		sort.Strings(c.order)
		c.seq++
		sum := sha256.Sum256([]byte(fmt.Sprintf("block-%d", c.seq)))
		c.blocks = append(c.blocks, block{hash: hex.EncodeToString(sum[:]), txids: txids})
		c.dropConflictsLocked()
	}
	epoch := lnclient.BlockEpoch{Height: c.height(), Hash: c.blocks[len(c.blocks)-1].hash}
	subs := append([]chan lnclient.BlockEpoch(nil), c.subs...)
	c.mu.Unlock()
	for _, s := range subs {
		select {
		case s <- epoch:
		default:
		}
	}
}

// dropConflictsLocked removes mempool transactions spending an outpoint a
// confirmed transaction spends, and then every descendant left without its
// parent.
func (c *Chain) dropConflictsLocked() {
	confirmed := map[string]string{}
	for _, b := range c.blocks {
		for _, id := range b.txids {
			for _, in := range c.txs[id].TxIn {
				confirmed[in.PreviousOutPoint.String()] = id
			}
		}
	}
	for changed := true; changed; {
		changed = false
		for id, m := range c.mempool {
			for _, in := range m.tx.TxIn {
				if in.PreviousOutPoint.Index == 0xffffffff {
					continue
				}
				prev := in.PreviousOutPoint.Hash.String()
				by, spentInChain := confirmed[in.PreviousOutPoint.String()]
				orphan := c.confHeightLocked(prev) == 0 && c.mempool[prev] == nil
				if (spentInChain && by != id) || orphan {
					delete(c.mempool, id)
					changed = true
					break
				}
			}
		}
	}
}

// Reorg removes the last depth blocks; their transactions return to the
// mempool unless drop names them (dropped ones are forgotten by the network).
func (c *Chain) Reorg(depth int, drop ...string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	dropped := map[string]bool{}
	for _, d := range drop {
		dropped[d] = true
	}
	for range depth {
		b := c.blocks[len(c.blocks)-1]
		c.blocks = c.blocks[:len(c.blocks)-1]
		for _, id := range b.txids {
			if dropped[id] {
				continue
			}
			m := &mempoolTx{tx: c.txs[id]}
			m.fee, m.vsize = c.feeLocked(m.tx), vsize(m.tx)
			c.mempool[id] = m
			c.order = append(c.order, id)
		}
	}
}

// Withhold keeps txid out of the next blocks (on) or lets it in (off).
func (c *Chain) Withhold(txid string, on bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if on {
		c.withheld[txid] = true
	} else {
		delete(c.withheld, txid)
	}
}

// Evict drops txid from the mempool, as a restarted node forgets it.
func (c *Chain) Evict(txid string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.mempool, txid)
}

// InMempool reports txid in the mempool.
func (c *Chain) InMempool(txid string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.mempool[txid]
	return ok
}

// Confirmed reports txid's confirmations (0: not in the chain).
func (c *Chain) Confirmations(txid string) int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	h := c.confHeightLocked(txid)
	if h == 0 {
		return 0
	}
	return c.height() - h + 1
}

// Tx is a known transaction.
func (c *Chain) Tx(txid string) *wire.MsgTx {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.txs[txid]
}

// ConfirmedPaying lists the confirmed transactions with an output of script and
// value.
func (c *Chain) ConfirmedPaying(script []byte, valueSat int64) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for _, b := range c.blocks {
		for _, id := range b.txids {
			for _, o := range c.txs[id].TxOut {
				if o.Value == valueSat && bytes.Equal(o.PkScript, script) {
					out = append(out, id)
				}
			}
		}
	}
	return out
}

// Height is the tip height.
func (c *Chain) Height() int64 { c.mu.Lock(); defer c.mu.Unlock(); return c.height() }

func (c *Chain) feeLocked(tx *wire.MsgTx) int64 {
	var in, out int64
	for _, i := range tx.TxIn {
		if o := c.outputs[i.PreviousOutPoint.String()]; o != nil {
			in += o.Value
		}
	}
	for _, o := range tx.TxOut {
		out += o.Value
	}
	return in - out
}

func vsize(tx *wire.MsgTx) int64 {
	base := int64(tx.SerializeSizeStripped())
	total := int64(tx.SerializeSize())
	weight := base*3 + total
	return (weight + 3) / 4
}

// --- lnclient.OnchainPayer ---

var _ lnclient.OnchainPayer = (*Chain)(nil)

func (c *Chain) ListUnspent(_ context.Context, minConfs int32, account string) ([]lnclient.Utxo, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if account != "" && account != "default" {
		return nil, nil
	}
	spent := c.spentLocked()
	var out []lnclient.Utxo
	for o, txo := range c.outputs {
		if !c.walletScripts[hex.EncodeToString(txo.PkScript)] || spent[o] != "" {
			continue
		}
		id := o[:64]
		ch := c.confHeightLocked(id)
		confs := int64(0)
		if ch > 0 {
			confs = c.height() - ch + 1
		}
		if confs < int64(minConfs) {
			continue
		}
		if c.mempool[id] == nil && ch == 0 {
			continue // its transaction left the network
		}
		out = append(out, lnclient.Utxo{Outpoint: o, ValueSat: txo.Value, Script: txo.PkScript, Confs: confs, Account: "default"})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Outpoint < out[j].Outpoint })
	return out, nil
}

// LeaseOutput leases a wallet output. Like the node's wallet, it refuses an
// output it does not know or one a mined transaction spent ("unknown
// output"); one spent only in the mempool is still leasable.
func (c *Chain) LeaseOutput(_ context.Context, id [32]byte, outpoint string, secs int64) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, known := c.outputs[outpoint]; !known {
		return 0, ErrUnknownOutput
	}
	if spender, spent := c.spentLocked()[outpoint]; spent && c.confHeightLocked(spender) > 0 {
		return 0, ErrUnknownOutput
	}
	now := c.now().Unix()
	if l, ok := c.leases[outpoint]; ok && l.expiry > now && l.id != id {
		return 0, errors.New("fakechain: output leased under another id")
	}
	exp := now + secs
	c.leases[outpoint] = lease{id: id, expiry: exp}
	return exp, nil
}

func (c *Chain) ReleaseOutput(_ context.Context, id [32]byte, outpoint string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if l, ok := c.leases[outpoint]; ok && l.id == id {
		delete(c.leases, outpoint)
	}
	return nil
}

func (c *Chain) ListLeases(context.Context) ([]lnclient.Lease, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now().Unix()
	var out []lnclient.Lease
	for o, l := range c.leases {
		if l.expiry > now {
			out = append(out, lnclient.Lease{Outpoint: o, LeaseID: l.id, Expiry: l.expiry})
		}
	}
	return out, nil
}

// LeaseForeign leases outpoint under another id, as another wallet user would.
func (c *Chain) LeaseForeign(outpoint string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.leases[outpoint] = lease{id: [32]byte{0xee}, expiry: c.now().Unix() + 86400}
}

// LeaseOf is outpoint's lease expiry under id, 0 for none.
func (c *Chain) LeaseOf(id [32]byte, outpoint string) int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	if l, ok := c.leases[outpoint]; ok && l.id == id {
		return l.expiry
	}
	return 0
}

func (c *Chain) FundPsbtFixed(_ context.Context, raw []byte, id [32]byte, lockSecs int64) ([]byte, error) {
	p, err := psbt.NewFromRawBytes(bytes.NewReader(raw), false)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now().Unix()
	for i, in := range p.UnsignedTx.TxIn {
		o := in.PreviousOutPoint.String()
		txo := c.outputs[o]
		if txo == nil || !c.walletScripts[hex.EncodeToString(txo.PkScript)] {
			return nil, fmt.Errorf("fakechain: input %s is not the wallet's", o)
		}
		if l, ok := c.leases[o]; ok && l.expiry > now && l.id != id {
			return nil, fmt.Errorf("fakechain: input %s leased under another id", o)
		}
		c.leases[o] = lease{id: id, expiry: now + lockSecs}
		p.Inputs[i].WitnessUtxo = txo
	}
	var buf bytes.Buffer
	if err := p.Serialize(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (c *Chain) FinalizePsbt(_ context.Context, raw []byte) ([]byte, error) {
	p, err := psbt.NewFromRawBytes(bytes.NewReader(raw), false)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	hook := c.OnFinalize
	c.FinalizeCalls++
	fail := c.FailFinalize > 0
	if fail {
		c.FailFinalize--
	}
	c.mu.Unlock()
	if hook != nil {
		hook()
	}
	if fail {
		return nil, errors.New("fakechain: finalize failed")
	}
	tx := p.UnsignedTx.Copy()
	for _, in := range tx.TxIn {
		sig := make([]byte, 72)
		pub := make([]byte, 33)
		in.Witness = wire.TxWitness{sig, pub}
	}
	var buf bytes.Buffer
	if err := tx.Serialize(&buf); err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.Finalized[tx.TxHash().String()]++
	c.mu.Unlock()
	return buf.Bytes(), nil
}

func decodeTx(raw []byte) (*wire.MsgTx, error) {
	var tx wire.MsgTx
	if err := tx.Deserialize(bytes.NewReader(raw)); err != nil {
		return nil, err
	}
	return &tx, nil
}

func (c *Chain) PublishTx(_ context.Context, raw []byte) error {
	tx, err := decodeTx(raw)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.publishLocked(tx)
}

func (c *Chain) publishLocked(tx *wire.MsgTx) error {
	id := tx.TxHash().String()
	if c.PublishHook != nil {
		if err := c.PublishHook(id); err != nil {
			return err
		}
	}
	if c.mempool[id] != nil || c.confHeightLocked(id) > 0 {
		return nil // already known
	}
	spent := c.spentLocked()
	conflicts := map[string]bool{}
	for _, in := range tx.TxIn {
		o := in.PreviousOutPoint.String()
		if c.outputs[o] == nil {
			return lnclient.ErrPublishInputGone
		}
		prev := in.PreviousOutPoint.Hash.String()
		if c.confHeightLocked(prev) == 0 && c.mempool[prev] == nil {
			return lnclient.ErrPublishInputGone
		}
		if by := spent[o]; by != "" {
			if c.confHeightLocked(by) > 0 {
				return lnclient.ErrPublishInputGone
			}
			conflicts[by] = true
		}
	}
	// Known outputs of tx, so its fee can be read and its children funded.
	for i, o := range tx.TxOut {
		c.outputs[op(id, uint32(i))] = o
	}
	fee, vs := c.feeLocked(tx), vsize(tx)
	minRelayVB := (c.MinRelay*4 + 999) / 1000
	if fee < minRelayVB*vs {
		return lnclient.ErrPublishFeeTooLow
	}
	if len(conflicts) > 0 {
		var oldFee, oldMaxRate int64
		for by := range conflicts {
			m := c.mempool[by]
			oldFee += m.fee
			oldMaxRate = max(oldMaxRate, m.fee*1000/max(m.vsize, 1))
		}
		if fee*1000/vs < oldMaxRate+IncrementalSatPerVB*1000 || fee < oldFee+IncrementalSatPerVB*vs {
			return lnclient.ErrPublishFeeTooLow
		}
		for by := range conflicts {
			delete(c.mempool, by)
		}
		c.dropConflictsLocked()
	}
	c.txs[id] = tx
	c.mempool[id] = &mempoolTx{tx: tx, fee: fee, vsize: vs}
	c.order = append(c.order, id)
	return nil
}

func (c *Chain) SubmitPackage(ctx context.Context, parent, child []byte) error {
	pt, err := decodeTx(parent)
	if err != nil {
		return err
	}
	ct, err := decodeTx(child)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	pid := pt.TxHash().String()
	if c.mempool[pid] == nil && c.confHeightLocked(pid) == 0 {
		// Accept the parent on the package's fee.
		for i, o := range pt.TxOut {
			c.outputs[op(pid, uint32(i))] = o
		}
		c.txs[pid] = pt
		c.mempool[pid] = &mempoolTx{tx: pt, fee: c.feeLocked(pt), vsize: vsize(pt)}
		c.order = append(c.order, pid)
	}
	return c.publishLocked(ct)
}

func (c *Chain) EstimateFee(context.Context, int) (lnclient.FeeEstimate, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return lnclient.FeeEstimate{SatPerKw: c.Estimate, MinRelaySatPerKw: c.MinRelay}, nil
}

func (c *Chain) RequiredReserve(context.Context) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.Reserve, nil
}

func (c *Chain) ListWalletScripts(context.Context) ([][]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out [][]byte
	for s := range c.walletScripts {
		b, _ := hex.DecodeString(s)
		out = append(out, b)
	}
	return out, nil
}

func (c *Chain) NextChangeScript(context.Context) ([]byte, error) { return c.WalletScript(), nil }

func (c *Chain) WalletTx(_ context.Context, txid string) (lnclient.WalletTx, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.WalletTxErr != nil {
		return lnclient.WalletTx{}, c.WalletTxErr
	}
	if c.txs[txid] == nil {
		return lnclient.WalletTx{}, nil
	}
	h := c.confHeightLocked(txid)
	if h == 0 {
		return lnclient.WalletTx{Found: c.mempool[txid] != nil}, nil
	}
	return lnclient.WalletTx{Found: true, Confs: c.height() - h + 1, BlockHeight: h,
		BlockHash: c.blocks[h-c.base-1].hash}, nil
}

func (c *Chain) ConfirmedBalance(context.Context) (int64, error) {
	utxos, _ := c.ListUnspent(context.TODO(), 1, "")
	var sum int64
	for _, u := range utxos {
		sum += u.ValueSat
	}
	return sum, nil
}

func (c *Chain) BestBlock(context.Context) (lnclient.BlockEpoch, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.blocks) == 0 {
		return lnclient.BlockEpoch{Height: c.base}, nil
	}
	return lnclient.BlockEpoch{Height: c.height(), Hash: c.blocks[len(c.blocks)-1].hash}, nil
}

func (c *Chain) SubscribeBlocks(ctx context.Context) (<-chan lnclient.BlockEpoch, error) {
	ch := make(chan lnclient.BlockEpoch, 16)
	c.mu.Lock()
	c.subs = append(c.subs, ch)
	c.mu.Unlock()
	return ch, nil
}

// SpendOutside mines, in a block of its own, a transaction of someone else's
// spending outpoint (the node's own spend, a sweep): every mempool transaction
// spending it is dropped. It returns the txid.
func (c *Chain) SpendOutside(outpoint string) string {
	c.mu.Lock()
	txo := c.outputs[outpoint]
	c.seq++
	tx := wire.NewMsgTx(2)
	h, idx := outpoint[:64], outpoint[65:]
	var hash [32]byte
	b, _ := hex.DecodeString(h)
	for i := range b {
		hash[i] = b[len(b)-1-i]
	}
	var n uint32
	fmt.Sscan(idx, &n)
	tx.AddTxIn(&wire.TxIn{PreviousOutPoint: wire.OutPoint{Hash: hash, Index: n}})
	sum := sha256.Sum256([]byte(fmt.Sprintf("outside-spend-%d", c.seq)))
	tx.AddTxOut(wire.NewTxOut(max(txo.Value-1000, 1), append([]byte{0x00, 0x14}, sum[:20]...)))
	id := tx.TxHash().String()
	c.txs[id] = tx
	c.outputs[op(id, 0)] = tx.TxOut[0]
	c.seq++
	bh := sha256.Sum256([]byte(fmt.Sprintf("block-%d", c.seq)))
	c.blocks = append(c.blocks, block{hash: hex.EncodeToString(bh[:]), txids: []string{id}})
	c.dropConflictsLocked()
	c.mu.Unlock()
	return id
}

// MempoolTxids lists the mempool.
func (c *Chain) MempoolTxids() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, 0, len(c.mempool))
	for id := range c.mempool {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// WithheldAll releases every withheld transaction.
func (c *Chain) ReleaseAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.withheld = map[string]bool{}
}
