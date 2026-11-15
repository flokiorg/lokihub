package api

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/tests"
)

// U-2.5 (admin path): the operator's sign-message route refuses the reserved
// prefixes as the NWC method does (NIP-CASH §Security Considerations), and still
// signs anything else.
func TestSignMessage_AdminRefusesReservedPrefixes(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	a := newTestAPIWithService(t, svc)

	for _, msg := range []string{"cash-mint:v1:lokicash:aa:1:bb", "CASH-ZAP:v1:x", "cash-hub:v1:x", "cash-order:v1:x", "Circle-Hub:v1:x"} {
		_, err := a.SignMessage(context.TODO(), msg)
		require.ErrorIs(t, err, ErrReservedSignText, msg)
	}
	res, err := a.SignMessage(context.TODO(), "hello")
	require.NoError(t, err)
	require.NotEmpty(t, res.Signature)
}
