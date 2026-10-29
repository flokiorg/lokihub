package cashorder

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBillOrdinal_CountsPastInt16AndRefusesPastInt32(t *testing.T) {
	for _, c := range []struct {
		recorded int64
		i        int
		want     int32
	}{{0, 0, 0}, {1, 2, 3}, {math.MaxInt16, 1, math.MaxInt16 + 1}, {0, math.MaxInt16 + 5, math.MaxInt16 + 5}, {math.MaxInt32 - 1, 1, math.MaxInt32}} {
		got, err := BillOrdinal(c.recorded, c.i)
		require.NoError(t, err)
		assert.Equal(t, c.want, got)
	}
	for _, c := range []struct {
		recorded int64
		i        int
	}{{math.MaxInt32, 1}, {-1, 0}, {0, -1}} {
		_, err := BillOrdinal(c.recorded, c.i)
		assert.Error(t, err)
	}
}
