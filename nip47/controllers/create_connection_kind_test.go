package controllers

// F3 — create_connection passes params.Kind verbatim to CreateApp with no
// allowlist, so a caller can create apps with privileged kinds (jit_hub,
// circle_hub, etc.) via NWC.
//
// Correct behaviour: only user-facing kinds (standard, isolated, …) must be
// accepted; privileged kinds must be rejected with an error response.
// This test FAILS today because CreateApp accepts any non-empty kind string.

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/flokiorg/lokihub/tests"
)

// privilegedKinds lists app kinds that must not be creatable via NWC create_connection.
var privilegedKinds = []string{
	db.AppKindJITHub,
	db.AppKindJITWallet,
	db.AppKindCircleHub,
	db.AppKindCircleWallet,
}

func TestHandleCreateConnectionEvent_PrivilegedKind_IsRejected(t *testing.T) {
	for _, kind := range privilegedKinds {
		kind := kind
		t.Run(kind, func(t *testing.T) {
			ctx := context.TODO()
			svc, err := tests.CreateTestService(t)
			require.NoError(t, err)
			defer svc.Remove()

			pairingSecretKey := nostr.GeneratePrivateKey()
			pairingPublicKey, err := nostr.GetPublicKey(pairingSecretKey)
			require.NoError(t, err)

			nip47JSON := fmt.Sprintf(`{
				"method": "create_connection",
				"params": {
					"pubkey": %q,
					"name": "evil app",
					"request_methods": ["get_info"],
					"kind": %q
				}
			}`, pairingPublicKey, kind)

			nip47Request := &models.Request{}
			require.NoError(t, json.Unmarshal([]byte(nip47JSON), nip47Request))

			dbRequestEvent := &db.RequestEvent{}
			require.NoError(t, svc.DB.Create(dbRequestEvent).Error)

			var publishedResponse *models.Response
			publishResponse := func(response *models.Response, tags nostr.Tags) {
				publishedResponse = response
			}

			NewTestNip47Controller(svc).
				HandleCreateConnectionEvent(ctx, nip47Request, dbRequestEvent.ID, publishResponse)

			// Correct behaviour: privileged kinds must be rejected.
			require.NotNil(t, publishedResponse, "handler must always publish a response")
			assert.NotNil(t, publishedResponse.Error,
				"kind=%q must be rejected via NWC create_connection; got success instead", kind)
			assert.Nil(t, publishedResponse.Result,
				"result must be nil when an error is returned for kind=%q", kind)

			// Verify that no app with the privileged kind was created.
			var count int64
			svc.DB.Model(&db.App{}).Where("kind = ?", kind).Count(&count)
			assert.Zero(t, count, "no app with kind=%q should exist after rejection", kind)
		})
	}
}
