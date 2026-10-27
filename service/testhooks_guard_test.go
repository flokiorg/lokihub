package service

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/flokiorg/lokihub/lnclient"
)

// A testhooks build starts beside a regtest node only; a release build is not
// checked here.
func TestTestBuildNetworkRefusal(t *testing.T) {
	assert.NoError(t, testBuildNetworkRefusal(false, &lnclient.NodeInfo{Network: "mainnet"}, nil))
	assert.NoError(t, testBuildNetworkRefusal(true, &lnclient.NodeInfo{Network: "regtest"}, nil))
	assert.Error(t, testBuildNetworkRefusal(true, &lnclient.NodeInfo{Network: "mainnet"}, nil))
	assert.Error(t, testBuildNetworkRefusal(true, &lnclient.NodeInfo{Network: "testnet"}, nil))
	assert.Error(t, testBuildNetworkRefusal(true, nil, errors.New("node unreachable")))
}
