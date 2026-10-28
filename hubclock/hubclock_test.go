package hubclock

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestNowRunsAheadByTheOffset(t *testing.T) {
	t.Cleanup(func() { setOffset(0) })
	assert.WithinDuration(t, time.Now(), Now(), time.Second)
	assert.Zero(t, Offset())

	m := Moved()
	setOffset(3600)
	select {
	case <-m:
	default:
		t.Fatal("moving the clock did not signal")
	}
	assert.EqualValues(t, 3600, Offset())
	assert.WithinDuration(t, time.Now().Add(time.Hour), Now(), time.Second)
	at := Now()
	assert.Equal(t, at.Add(-time.Hour), Wall(at))

	setOffset(0)
	assert.Equal(t, at, Wall(at))
}
