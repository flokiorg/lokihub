//go:build integration

package integration

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// setFaults sets the injected faults (POST /api/test/faults), clearing them all
// again when the test ends.
func (h *stackHooks) setFaults(t *testing.T, faults map[string]bool) {
	t.Helper()
	body := map[string]any{}
	for k, v := range faults {
		body[k] = v
	}
	require.NoError(t, h.admin.doBody(http.MethodPost, "/api/test/faults", body, nil))
	t.Cleanup(func() { _ = h.admin.doBody(http.MethodPost, "/api/test/faults", map[string]any{"reset": true}, nil) })
}

// hubClockOffset moves every clock the Hub's protocol state reads, putting it
// back when the test ends.
func (h *stackHooks) hubClockOffset(t *testing.T, secs int64) {
	t.Helper()
	require.NoError(t, h.admin.doBody(http.MethodPost, "/api/test/clock", map[string]any{"offset_secs": secs, "scope": "all"}, nil))
	t.Cleanup(func() {
		_ = h.admin.doBody(http.MethodPost, "/api/test/clock", map[string]any{"offset_secs": 0, "scope": "all"}, nil)
	})
}
