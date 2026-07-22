package decodepay

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestClampIntToUint64(t *testing.T) {
	assert.Equal(t, uint64(0), clampIntToUint64(0))
	assert.Equal(t, uint64(1000), clampIntToUint64(1000))
	assert.Equal(t, uint64(0), clampIntToUint64(-1))
	assert.Equal(t, uint64(0), clampIntToUint64(math.MinInt))
	assert.Equal(t, uint64(math.MaxInt), clampIntToUint64(math.MaxInt))
}

func TestClampIntToUint32(t *testing.T) {
	assert.Equal(t, uint32(0), clampIntToUint32(0))
	assert.Equal(t, uint32(1000), clampIntToUint32(1000))
	assert.Equal(t, uint32(0), clampIntToUint32(-1))
	assert.Equal(t, uint32(0), clampIntToUint32(math.MinInt))
	assert.Equal(t, uint32(math.MaxUint32), clampIntToUint32(math.MaxUint32))
	assert.Equal(t, uint32(math.MaxUint32), clampIntToUint32(math.MaxUint32+1))
	assert.Equal(t, uint32(math.MaxUint32), clampIntToUint32(math.MaxInt))
}
