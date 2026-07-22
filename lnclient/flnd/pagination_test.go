package flnd

import (
	"context"
	"math"
	"testing"

	"github.com/flokiorg/lokihub/lnclient"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestServiceWithCache(cache []lnclient.OnchainTransaction) *FLNDService {
	return &FLNDService{
		txCache:      cache,
		txCacheValid: true,
	}
}

func TestListOnchainTransactions_NormalPagination(t *testing.T) {
	cache := []lnclient.OnchainTransaction{
		{TxId: "a"}, {TxId: "b"}, {TxId: "c"}, {TxId: "d"}, {TxId: "e"},
	}
	svc := newTestServiceWithCache(cache)

	result, err := svc.ListOnchainTransactions(context.Background(), 0, 0, 2, 1)
	require.NoError(t, err)
	require.Len(t, result, 2)
	assert.Equal(t, "b", result[0].TxId)
	assert.Equal(t, "c", result[1].TxId)
}

func TestListOnchainTransactions_ZeroLimitReturnsRest(t *testing.T) {
	cache := []lnclient.OnchainTransaction{
		{TxId: "a"}, {TxId: "b"}, {TxId: "c"},
	}
	svc := newTestServiceWithCache(cache)

	result, err := svc.ListOnchainTransactions(context.Background(), 0, 0, 0, 1)
	require.NoError(t, err)
	require.Len(t, result, 2)
	assert.Equal(t, "b", result[0].TxId)
	assert.Equal(t, "c", result[1].TxId)
}

// A pre-fix version of this logic converted offset/limit straight to int,
// so a uint64 near math.MaxUint64 would wrap to a negative int and either
// panic (negative slice index) or attempt a multi-exabyte allocation in
// make(). This guards against that regressing.
func TestListOnchainTransactions_HugeOffsetDoesNotPanic(t *testing.T) {
	cache := []lnclient.OnchainTransaction{{TxId: "a"}, {TxId: "b"}}
	svc := newTestServiceWithCache(cache)

	result, err := svc.ListOnchainTransactions(context.Background(), 0, 0, 0, math.MaxUint64)
	require.NoError(t, err)
	assert.Empty(t, result)
}

func TestListOnchainTransactions_HugeLimitDoesNotPanic(t *testing.T) {
	cache := []lnclient.OnchainTransaction{{TxId: "a"}, {TxId: "b"}, {TxId: "c"}}
	svc := newTestServiceWithCache(cache)

	result, err := svc.ListOnchainTransactions(context.Background(), 0, 0, math.MaxUint64, 1)
	require.NoError(t, err)
	require.Len(t, result, 2)
	assert.Equal(t, "b", result[0].TxId)
	assert.Equal(t, "c", result[1].TxId)
}

func TestListOnchainTransactions_HugeOffsetAndLimitDoesNotPanic(t *testing.T) {
	cache := []lnclient.OnchainTransaction{{TxId: "a"}, {TxId: "b"}}
	svc := newTestServiceWithCache(cache)

	result, err := svc.ListOnchainTransactions(context.Background(), 0, 0, math.MaxUint64, math.MaxUint64)
	require.NoError(t, err)
	assert.Empty(t, result)
}

func TestClampUint64ToUint32(t *testing.T) {
	assert.Equal(t, uint32(0), clampUint64ToUint32(0))
	assert.Equal(t, uint32(1000), clampUint64ToUint32(1000))
	assert.Equal(t, uint32(math.MaxUint32), clampUint64ToUint32(math.MaxUint32))
	assert.Equal(t, uint32(math.MaxUint32), clampUint64ToUint32(math.MaxUint32+1))
	assert.Equal(t, uint32(math.MaxUint32), clampUint64ToUint32(math.MaxUint64))
}

func TestClampInt64ToUint32(t *testing.T) {
	assert.Equal(t, uint32(0), clampInt64ToUint32(0))
	assert.Equal(t, uint32(1000), clampInt64ToUint32(1000))
	assert.Equal(t, uint32(0), clampInt64ToUint32(-1))
	assert.Equal(t, uint32(0), clampInt64ToUint32(math.MinInt64))
	assert.Equal(t, uint32(math.MaxUint32), clampInt64ToUint32(math.MaxUint32))
	assert.Equal(t, uint32(math.MaxUint32), clampInt64ToUint32(math.MaxUint32+1))
	assert.Equal(t, uint32(math.MaxUint32), clampInt64ToUint32(math.MaxInt64))
}
