package flnd

import (
	"bytes"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/flokiorg/flnd/lnrpc"
	"github.com/flokiorg/go-flokicoin/chaincfg/chainhash"
	"github.com/flokiorg/go-flokicoin/chainutil/psbt"
	"github.com/flokiorg/go-flokicoin/txscript"
	"github.com/flokiorg/go-flokicoin/wire"

	"github.com/stretchr/testify/assert"

	"github.com/flokiorg/lokihub/lnclient"
)

// The node's publish refusals map to the classes the payout engine acts on.
func TestClassifyPublish(t *testing.T) {
	assert.NoError(t, classifyPublish(""))
	assert.NoError(t, classifyPublish("txn-already-in-mempool"))
	assert.NoError(t, classifyPublish("transaction already have"))
	assert.ErrorIs(t, classifyPublish("min relay fee not met"), lnclient.ErrPublishFeeTooLow)
	assert.ErrorIs(t, classifyPublish("insufficient fee, rejecting replacement"), lnclient.ErrPublishFeeTooLow)
	assert.ErrorIs(t, classifyPublish("bad-txns-inputs-missingorspent"), lnclient.ErrPublishInputGone)
	err := classifyPublish("something else")
	assert.Error(t, err)
	assert.NotErrorIs(t, err, lnclient.ErrPublishFeeTooLow)
}

// A transaction the wallet does not hold is no error for WalletTx (live run22:
// flnd answers "can not find transaction" for a version never published).
func TestTxUnknown(t *testing.T) {
	for _, msg := range []string{"rpc error: code = Unknown desc = can not find transaction: txid 5ef5", "tx not found", "cannot find tx"} {
		if !txUnknown(errors.New(msg)) {
			t.Errorf("%q should read as unknown", msg)
		}
	}
	if txUnknown(errors.New("connection refused")) {
		t.Error("a transport error is an error")
	}
}

// A segwit v0 payout input is signed SIGHASH_ALL, a taproot one SIGHASH_DEFAULT.
func TestSighashFor(t *testing.T) {
	p2wkh := append([]byte{0x00, 0x14}, make([]byte, 20)...)
	p2tr := append([]byte{0x51, 0x20}, make([]byte, 32)...)
	if got := sighashFor(p2wkh); got != txscript.SigHashAll {
		t.Errorf("p2wkh: %v", got)
	}
	if got := sighashFor(p2tr); got != txscript.SigHashDefault {
		t.Errorf("p2tr: %v", got)
	}
}

// fundTemplate is a PSBT spending ins, paying one output.
func fundTemplate(t *testing.T, ins ...wire.OutPoint) *psbt.Packet {
	t.Helper()
	tx := wire.NewMsgTx(2)
	for i := range ins {
		tx.AddTxIn(wire.NewTxIn(&ins[i], nil, nil))
	}
	tx.AddTxOut(wire.NewTxOut(1000, []byte{0x00, 0x14, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20}))
	p, err := psbt.NewFromUnsignedTx(tx)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func outpointN(b byte, n uint32) wire.OutPoint {
	var h chainhash.Hash
	h[0] = b
	return wire.OutPoint{Hash: h, Index: n}
}

// A replacement re-uses a broadcast version's inputs, which the wallet no
// longer lists as unspent: they are decorated from the parent transaction and
// leased like a listed coin.
func TestFundFixedSpentByOurVersion(t *testing.T) {
	listed, spent := outpointN(1, 0), outpointN(2, 1)
	v0 := []byte{0x00, 0x14, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9}
	p := fundTemplate(t, listed, spent)
	var leased []string
	err := fundFixed(p, []lnclient.Utxo{{Outpoint: listed.String(), ValueSat: 5000, Script: v0}}, nil,
		func(op wire.OutPoint) (*wire.TxOut, error) {
			if op != spent {
				t.Fatalf("parent lookup for a listed coin %s", op)
			}
			return wire.NewTxOut(7000, v0), nil
		},
		func(o string) error { leased = append(leased, o); return nil },
		func(o string) { t.Fatalf("released %s on success", o) })
	assert.NoError(t, err)
	assert.Equal(t, []string{listed.String(), spent.String()}, leased)
	assert.Equal(t, int64(5000), p.Inputs[0].WitnessUtxo.Value)
	assert.Equal(t, int64(7000), p.Inputs[1].WitnessUtxo.Value)
	assert.Equal(t, txscript.SigHashAll, p.Inputs[1].SighashType)
}

// An input that fails releases only the leases the call itself took, never
// one held under the id before it.
func TestFundFixedReleasesOwnLeasesOnFailure(t *testing.T) {
	held, fresh, foreign := outpointN(1, 0), outpointN(2, 0), outpointN(3, 0)
	v0 := []byte{0x00, 0x14, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9}
	p := fundTemplate(t, held, fresh, foreign)
	var released []string
	err := fundFixed(p, []lnclient.Utxo{{Outpoint: held.String(), ValueSat: 1, Script: v0}, {Outpoint: fresh.String(), ValueSat: 1, Script: v0}},
		map[string]bool{held.String(): true},
		func(wire.OutPoint) (*wire.TxOut, error) { return nil, errNotOwnOutput },
		func(string) error { return nil },
		func(o string) { released = append(released, o) })
	assert.ErrorIs(t, err, errNotOwnOutput)
	assert.Equal(t, []string{fresh.String()}, released)

	// A lease the wallet refuses ("unknown output": a coin a confirmed
	// transaction spent) fails the call the same way.
	p = fundTemplate(t, fresh, held)
	released = nil
	err = fundFixed(p, []lnclient.Utxo{{Outpoint: held.String(), ValueSat: 1, Script: v0}, {Outpoint: fresh.String(), ValueSat: 1, Script: v0}}, nil,
		nil,
		func(o string) error {
			if o == held.String() {
				return errors.New("unknown output")
			}
			return nil
		},
		func(o string) { released = append(released, o) })
	assert.Error(t, err)
	assert.Equal(t, []string{fresh.String()}, released)
}

// parentOutput reads the output from the raw transaction, only when the
// wallet marks it its own.
func TestParentOutput(t *testing.T) {
	v0 := []byte{0x00, 0x14, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9}
	tx := wire.NewMsgTx(2)
	op := outpointN(1, 0)
	tx.AddTxIn(wire.NewTxIn(&op, nil, nil))
	tx.AddTxOut(wire.NewTxOut(100, []byte{0x51}))
	tx.AddTxOut(wire.NewTxOut(4242, v0))
	var buf bytes.Buffer
	if err := tx.Serialize(&buf); err != nil {
		t.Fatal(err)
	}
	rt := &lnrpc.Transaction{RawTxHex: hex.EncodeToString(buf.Bytes()), OutputDetails: []*lnrpc.OutputDetail{
		{OutputIndex: 0, IsOurAddress: false}, {OutputIndex: 1, IsOurAddress: true}}}
	out, err := parentOutput(rt, 1)
	assert.NoError(t, err)
	assert.Equal(t, int64(4242), out.Value)
	_, err = parentOutput(rt, 0)
	assert.ErrorIs(t, err, errNotOwnOutput)
	_, err = parentOutput(rt, 2)
	assert.Error(t, err)
}
