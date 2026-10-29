package nip47

import (
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"

	"github.com/flokiorg/lokihub/nip47/models"
)

// WP-1 item 3: a standard request's deadline is its expiration tag, read as
// nmilat's RequestExpiration reads it; arrival is the time it was handled.
func TestStandardCallMeta(t *testing.T) {
	at := time.Unix(1_800_000_000, 0)
	ev := func(tags ...nostr.Tag) *nostr.Event { return &nostr.Event{Tags: tags} }

	m := standardCallMeta(ev(nostr.Tag{"p", "x"}), at)
	assert.Equal(t, models.TransportStandard, m.Transport)
	assert.Equal(t, at.Unix(), m.ArrivedAt)
	assert.False(t, m.DeadlinePresent)
	assert.False(t, m.DeadlineMalformed)

	m = standardCallMeta(ev(nostr.Tag{"expiration", "1800000060"}), at)
	assert.True(t, m.DeadlinePresent)
	assert.EqualValues(t, 1800000060, m.Deadline)

	for _, bad := range []*nostr.Event{
		ev(nostr.Tag{"expiration", "1"}, nostr.Tag{"expiration", "2"}),
		ev(nostr.Tag{"expiration", "0123"}),
		ev(nostr.Tag{"expiration", "-5"}),
	} {
		assert.True(t, standardCallMeta(bad, at).DeadlineMalformed, "%v", bad.Tags)
	}
}
