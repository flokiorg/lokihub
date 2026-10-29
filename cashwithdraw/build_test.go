package cashwithdraw

// NIP-CASH §Paying a Payout (CASH-79): a signed version is stored, and broadcast, only when it is
// exactly the about-to-sign record (its inputs, and every output's script and value).

import (
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/flokiorg/go-flokicoin/chaincfg/chainhash"
	"github.com/flokiorg/go-flokicoin/wire"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/db"
)

func TestMatchesRecord_OnlyTheExactRecordedVersion(t *testing.T) {
	h1, h2 := chainhash.Hash{1}, chainhash.Hash{2}
	payScript := append([]byte{0x00, 0x14}, make([]byte, 20)...)
	changeScript := append([]byte{0x00, 0x14}, bytesOf(0x22, 20)...)
	id := uint(7)
	build := func() *wire.MsgTx {
		tx := wire.NewMsgTx(2)
		tx.AddTxIn(wire.NewTxIn(wire.NewOutPoint(&h1, 0), nil, nil))
		tx.AddTxIn(wire.NewTxIn(wire.NewOutPoint(&h2, 3), nil, nil))
		tx.AddTxOut(wire.NewTxOut(19_258, payScript))
		tx.AddTxOut(wire.NewTxOut(50_000, changeScript))
		return tx
	}
	ins, _ := json.Marshal([]string{wire.NewOutPoint(&h1, 0).String(), wire.NewOutPoint(&h2, 3).String()})
	outs, _ := json.Marshal([]vout{{Script: hex.EncodeToString(payScript), Value: 19_258, WithdrawalID: &id},
		{Script: hex.EncodeToString(changeScript), Value: 50_000}})
	v := &db.CashWithdrawVersion{Inputs: string(ins), Outputs: string(outs)}
	require.True(t, matchesRecord(build(), v))

	lower := build()
	lower.TxOut[0].Value--
	assert.False(t, matchesRecord(lower, v), "a payout output one base unit short")

	redirected := build()
	redirected.TxOut[0].PkScript = changeScript
	assert.False(t, matchesRecord(redirected, v), "a payout output paying another script")

	extra := build()
	extra.AddTxOut(wire.NewTxOut(1, payScript))
	assert.False(t, matchesRecord(extra, v), "an output the record does not hold")

	swapped := build()
	swapped.TxIn[0], swapped.TxIn[1] = swapped.TxIn[1], swapped.TxIn[0]
	assert.False(t, matchesRecord(swapped, v), "inputs in another order")

	dropped := build()
	dropped.TxIn = dropped.TxIn[:1]
	assert.False(t, matchesRecord(dropped, v), "an input of the record left out")
}

func bytesOf(b byte, n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = b
	}
	return out
}
