package flnd

import (
	"errors"
	"testing"

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
