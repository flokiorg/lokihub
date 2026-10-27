package flnd

import (
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
