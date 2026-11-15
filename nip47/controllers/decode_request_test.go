package controllers

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/ohstr/nmilat/nipcash"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/logger"
	"github.com/flokiorg/lokihub/nip47/models"
)

// CASH-data-model-12: a presented cash secret must not be persisted in a form
// that discloses it — a log line included. The proofs ride along for the same
// reason.
func TestDecodeRequest_FailureDoesNotLogParams(t *testing.T) {
	var logs bytes.Buffer
	saved := logger.Logger
	logger.Logger = zerolog.New(&logs)
	t.Cleanup(func() { logger.Logger = saved })

	const secret = "c0ffeec0ffeec0ffeec0ffeec0ffeec0ffeec0ffeec0ffeec0ffeec0ffeec0ff"
	const identityEvent = `{"kind":23198,"sig":"identity-proof-marker"}`
	const attestationEvent = `{"kind":23199,"sig":"attestation-proof-marker"}`
	params, err := json.Marshal(map[string]interface{}{
		"invoice":           "lnfc1",
		"amount":            "1000", // the only defect: a string where a number belongs
		"cash_secret":       secret,
		"identity_event":    identityEvent,
		"attestation_event": attestationEvent,
	})
	require.NoError(t, err)

	resp := decodeRequest(&models.Request{Method: "cash_redeem", Params: params}, &nipcash.CashRedeemRequest{})
	require.NotNil(t, resp, "the request is refused")

	logged := logs.String()
	require.Contains(t, logged, "cash_redeem", "the failure is still logged with its method")
	for _, leaked := range []string{secret, "identity-proof-marker", "attestation-proof-marker"} {
		require.NotContains(t, logged, leaked)
	}
}
