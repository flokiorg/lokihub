package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/lnclient"
)

func TestEnforceDevStackNetwork(t *testing.T) {
	code := -1
	prev := exitProcess
	exitProcess = func(c int) { code = c }
	t.Cleanup(func() { exitProcess = prev })

	run := func(devStack bool, info *lnclient.NodeInfo, infoErr error) (int, error) {
		code = -1
		ln := &fakeNodeInfo{info: info, err: infoErr}
		err := enforceDevStackNetwork(context.Background(), ln, devStack)
		if !devStack {
			require.Zero(t, ln.calls)
		}
		return code, err
	}

	c, err := run(false, nil, nil)
	require.NoError(t, err)
	require.Equal(t, -1, c, "outside the dev stack the node is not asked")

	c, err = run(true, &lnclient.NodeInfo{Network: "regtest"}, nil)
	require.NoError(t, err)
	require.Equal(t, -1, c)

	for _, n := range []string{"bitcoin", "testnet", "signet"} {
		c, err = run(true, &lnclient.NodeInfo{Network: n}, nil)
		require.Error(t, err, n)
		require.Equal(t, 1, c, n)
	}

	c, err = run(true, nil, errors.New("down"))
	require.Error(t, err, "a node that cannot say its network is refused")
	require.Equal(t, 1, c)
}

type fakeNodeInfo struct {
	info  *lnclient.NodeInfo
	err   error
	calls int
}

func (f *fakeNodeInfo) GetInfo(context.Context) (*lnclient.NodeInfo, error) {
	f.calls++
	return f.info, f.err
}
