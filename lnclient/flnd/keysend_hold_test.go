package flnd

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/flokiorg/flnd/lnrpc"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"google.golang.org/grpc"

	"github.com/flokiorg/lokihub/lnclient/flnd/wrapper"
)

// debugInfoLightning answers GetDebugInfo with config, or err.
type debugInfoLightning struct {
	lnrpc.LightningClient
	config map[string]string
	err    error
}

func (f *debugInfoLightning) GetDebugInfo(_ context.Context, _ *lnrpc.GetDebugInfoRequest, _ ...grpc.CallOption) (*lnrpc.GetDebugInfoResponse, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &lnrpc.GetDebugInfoResponse{Config: f.config}, nil
}

func keysendHoldLog(ln *debugInfoLightning) string {
	var out bytes.Buffer
	svc := &FLNDService{client: wrapper.NewFLNDWrapperWithClients(ln, &fakeRouter{}), logger: zerolog.New(&out)}
	svc.checkKeysendHoldTime(context.Background())
	return out.String()
}

// A node that accepts keysends and settles them itself is warned about at
// start (NIP-CW §Own Funds and Caps: an over-room keysend MUST fail, never be
// credited); one that holds them, or takes none, is not; and a config that
// cannot be read never stops the Hub.
func TestCheckKeysendHoldTime(t *testing.T) {
	warned := keysendHoldLog(&debugInfoLightning{config: map[string]string{"accept-keysend": "true", "keysend-hold-time": "0s"}})
	assert.Contains(t, warned, `"level":"warn"`)
	assert.Contains(t, warned, "keysend-hold-time")

	unset := keysendHoldLog(&debugInfoLightning{config: map[string]string{"accept-keysend": "true"}})
	assert.Contains(t, unset, "without keysend-hold-time")

	assert.Empty(t, keysendHoldLog(&debugInfoLightning{config: map[string]string{"accept-keysend": "true", "keysend-hold-time": "30s"}}))
	assert.Empty(t, keysendHoldLog(&debugInfoLightning{config: map[string]string{"accept-keysend": "false", "keysend-hold-time": "0s"}}))

	failed := keysendHoldLog(&debugInfoLightning{err: errors.New("permission denied")})
	assert.Contains(t, failed, "Could not read the node's config")
}
