package cashwithdraw

import (
	"bytes"
	"encoding/hex"
	"testing"

	"github.com/flokiorg/go-flokicoin/wire"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/lnclient"
)

// R2-P-L2: before a bump is broadcast, a payout's tx is still the version its
// txid names.
func TestR2_TxMatchesTxid(t *testing.T) {
	w := newEngineWorld(t, 5)
	wd := w.commitTo(t, 21_000_000)
	w.passAfterWindow(t)
	first := *w.payout(t, wd.ID).Txid
	w.hooks.HoldBroadcast.Store(true)
	w.chain.Withhold(first, true)
	w.chain.Mine(7)
	w.pass(t) // bumped: a second version, signed, not broadcast
	vs := w.versions(t)
	require.Len(t, vs, 2)
	require.Equal(t, db.CashVersionSigned, vs[1].State)

	raw, err := w.eng.PayoutTxHexTx(w.svc.DB, wd.ID)
	require.NoError(t, err)
	b, err := hex.DecodeString(raw)
	require.NoError(t, err)
	var tx wire.MsgTx
	require.NoError(t, tx.Deserialize(bytes.NewReader(b)))
	require.Equal(t, first, tx.TxHash().String(), "tx is the version txid names")
}

// R2-P-L3: a signed payout's set-aside is not counted again against the coins
// left, once its own coins are in a version.
func TestR2_SignedSetAsideCountedOnce(t *testing.T) {
	w := newEngineWorld(t, 0)
	w.chain.Fund(1_000_000, 0)
	w.chain.Fund(1_000_000, 0)
	w.chain.Mine(10)
	w.pass(t)
	w.hooks.UnconfirmedBoundMillis.Store(1 << 50)
	wd := w.commitTo(t, 600_000_000)
	w.passAfterWindow(t)
	require.True(t, w.payout(t, wd.ID).Signed)
	ok, err := w.eng.CanSetAsideTx(w.svc.DB, 700_000_000, 0)
	require.NoError(t, err)
	require.True(t, ok, "the other coin is free; the signed payout's coin is in its version")
}

// R2-P-L1: a child whose package submit failed is submitted again by the next
// pass, not left signed forever.
func TestR2_ChildPackageRetried(t *testing.T) {
	w := newEngineWorld(t, 5)
	wd := w.commitTo(t, 21_000_000)
	w.passAfterWindow(t)
	txid := *w.payout(t, wd.ID).Txid
	w.chain.Mine(1)
	w.pass(t)
	w.chain.Reorg(1, txid)
	w.pass(t) // republished, broadcast again

	// The child is refused as a package and on its own (the fallback for a
	// node without package relay) until the hook is cleared.
	refused := false
	w.chain.PublishHook = func(id string) error {
		if id != txid {
			refused = true
			return lnclient.ErrPublishFeeTooLow
		}
		return nil
	}
	w.chain.Withhold(txid, true)
	w.chain.Mine(7)
	w.pass(t)
	require.True(t, refused)
	var child db.CashWithdrawVersion
	for _, v := range w.versions(t) {
		if v.Kind == db.CashVersionChild {
			child = v
		}
	}
	require.Equal(t, db.CashVersionSigned, child.State, "the package was refused")

	w.chain.PublishHook = nil
	w.pass(t)
	for _, v := range w.versions(t) {
		if v.ID == child.ID {
			require.Equal(t, db.CashVersionBroadcast, v.State, "submitted again")
		}
	}
	require.True(t, w.chain.InMempool(*child.Txid))
}
