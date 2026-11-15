package numeric

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestClampUint64ToInt(t *testing.T) {
	assert.Equal(t, 0, ClampUint64ToInt(0))
	assert.Equal(t, 1000, ClampUint64ToInt(1000))
	assert.Equal(t, math.MaxInt, ClampUint64ToInt(math.MaxInt))
	assert.Equal(t, math.MaxInt, ClampUint64ToInt(uint64(math.MaxInt)+1))
	assert.Equal(t, math.MaxInt, ClampUint64ToInt(math.MaxUint64))
}
