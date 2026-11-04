package sliceutil_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/flokiorg/lokihub/sliceutil"
)

func TestFilter(t *testing.T) {
	even := func(n int) bool { return n%2 == 0 }

	assert.Equal(t, []int{2, 4}, sliceutil.Filter([]int{1, 2, 3, 4}, even))
	// No match is a nil slice, not an empty one: callers serialise the result.
	assert.Nil(t, sliceutil.Filter([]int{1, 3}, even))
	assert.Nil(t, sliceutil.Filter([]int(nil), even))
}
