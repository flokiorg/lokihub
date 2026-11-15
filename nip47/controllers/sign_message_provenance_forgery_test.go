package controllers

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/flokiorg/go-flokicoin/crypto"
	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tv42/zbase32"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/flokiorg/lokihub/tests"
	"github.com/ohstr/nmilat/nipcash"
)

// A wallet pubkey the attacker does not control and an amount they were never
// allocated — the two things a forged provenance would attest to.
const (
	forgeryVictimWalletPubkey = "aa" + "bb112233445566778899aabbccddeeff00112233445566778899aabbccddee"
	forgeryInflatedAmount     = uint64(999_999_999_999)
)

type signMessageTestSetup struct {
	ctx            context.Context
	svc            *tests.TestService
	dbRequestEvent *db.RequestEvent
	hubNodePubkey  string
	publishCalled  bool
	response       *models.Response
}

func (s *signMessageTestSetup) PublishResponse(response *models.Response, _ nostr.Tags) {
	s.publishCalled = true
	s.response = response
}

func (s *signMessageTestSetup) TearDown() { s.svc.Remove() }

// setupSignMessageTest wires a test service whose mock LN client signs with a
// real key, exactly as flnd's node would (prefix + double-SHA256 + compact
// recoverable + zbase32), so provenance forgery can be attempted for real
// rather than simulated.
func setupSignMessageTest(t *testing.T) *signMessageTestSetup {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)

	priv, err := crypto.NewPrivateKey()
	require.NoError(t, err)
	hubNodePubkey := hex.EncodeToString(priv.PubKey().SerializeCompressed())

	mockLN := svc.LNClient.(*tests.MockLn)
	mockLN.SigningKey = priv
	mockLN.Pubkey = hubNodePubkey

	dbRequestEvent := &db.RequestEvent{}
	require.NoError(t, svc.DB.Create(&dbRequestEvent).Error)

	return &signMessageTestSetup{
		ctx:            context.TODO(),
		svc:            svc,
		dbRequestEvent: dbRequestEvent,
		hubNodePubkey:  hubNodePubkey,
	}
}

func signMessageRequest(t *testing.T, message string) *models.Request {
	t.Helper()
	params, err := json.Marshal(map[string]string{"message": message})
	require.NoError(t, err)
	req := &models.Request{}
	body, err := json.Marshal(map[string]any{
		"method": "sign_message",
		"params": json.RawMessage(params),
	})
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(body, req))
	return req
}

// forgeryRelays and forgerySecret are the hints and connection secret of the
// forged bill; the cash-mint:v1 payload binds both through relay_hash.
var (
	forgeryRelays = []string{"wss://relay.example.com"}
	forgerySecret = strings.Repeat("5a", 32)
)

func forgeryPayload(t *testing.T) string {
	t.Helper()
	relayHash, err := nipcash.RelayHash(forgeryRelays, forgerySecret)
	require.NoError(t, err)
	return nipcash.MintPayloadV1(constants.CashTokenHRP, forgeryVictimWalletPubkey, forgeryInflatedAmount, relayHash)
}

// U-2.5: an app holding the sign_message scope must not borrow the node key to
// sign a text the protocol signs (NIP-CASH §Security Considerations): a bill, a
// zap bill, or a cash hub, order or circle hub connection string.
func TestSignMessage_RefusesReservedPrefixes(t *testing.T) {
	for name, message := range map[string]string{
		"mintPayload":  "", // filled below
		"zap":          "cash-zap:v1:lokicash:deadbeef",
		"cashHub":      "cash-hub:v1:flokicoin:aa:bb:cc:dd",
		"cashOrder":    "cash-order:v1:flokicoin:aa:bb:cc:dd",
		"circleHub":    "circle-hub:v1:flokicoin:aa:bb:cc:dd",
		"upper":        "CASH-MINT:V1:LOKICASH:AA:1:BB",
		"mixedcase":    "Cash-Hub:v1:x",
		"prefixOnly":   "cash-mint:",
		"circlePrefix": "circle-hub:",
	} {
		t.Run(name, func(t *testing.T) {
			setup := setupSignMessageTest(t)
			defer setup.TearDown()
			if message == "" {
				message = forgeryPayload(t)
			}

			NewTestNip47Controller(setup.svc).
				HandleSignMessageEvent(setup.ctx, signMessageRequest(t, message),
					setup.dbRequestEvent.ID, setup.PublishResponse)

			require.True(t, setup.publishCalled)
			require.NotNil(t, setup.response.Error, "%q must be refused", message)
			assert.Equal(t, constants.ERROR_RESTRICTED, setup.response.Error.Code)
			// No signature may leak, not even alongside an error.
			assert.Nil(t, setup.response.Result)
		})
	}
}

// TestSignMessage_MintPayloadIsGenuinelyForgeable keeps the test above from going
// vacuous: the refused payload, signed through the very same node primitive,
// yields a bill whose provenance verifies as this hub's own, for a wallet the
// signer never minted and an amount it never attested.
func TestSignMessage_MintPayloadIsGenuinelyForgeable(t *testing.T) {
	setup := setupSignMessageTest(t)
	defer setup.TearDown()

	// Bypass the controller and hit the node primitive directly — this is what
	// the guard is denying access to.
	zsig, err := setup.svc.LNClient.SignMessage(setup.ctx, forgeryPayload(t))
	require.NoError(t, err)
	raw, err := zbase32.DecodeString(zsig)
	require.NoError(t, err)
	require.Len(t, raw, 65, "a node signature is a 65-byte recoverable compact sig")

	amount := forgeryInflatedAmount
	forged := nipcash.Token{
		HRP:                  constants.CashTokenHRP,
		WalletPubkey:         forgeryVictimWalletPubkey,
		Secret:               forgerySecret,
		RelayURLs:            forgeryRelays,
		MintSignature:        raw,
		AttestedAmountMillis: &amount,
		Scheme:               nipcash.SchemeMintV1,
	}

	recovered, ok := nipcash.VerifyProvenance(forged)
	require.True(t, ok, "the forged token must verify — otherwise the guard defends nothing")
	assert.Equal(t, setup.hubNodePubkey, recovered,
		"forged provenance recovers to this hub's node key, which is exactly the attack")
}

// TestSignMessage_AllowsOrdinaryMessage guards against over-blocking: the method
// keeps working for everything that does not start with a reserved label.
func TestSignMessage_AllowsOrdinaryMessage(t *testing.T) {
	for name, message := range map[string]string{
		"plain":              "hello world",
		"mentionsLokicash":   "paying you in lokicash today",
		"prefixNotAtStart":   "see also cash-mint:v1:lokicash:deadbeef:1",
		"similarButDistinct": "cash-minted:v1:lokicash:deadbeef:1",
	} {
		t.Run(name, func(t *testing.T) {
			setup := setupSignMessageTest(t)
			defer setup.TearDown()

			NewTestNip47Controller(setup.svc).
				HandleSignMessageEvent(setup.ctx, signMessageRequest(t, message),
					setup.dbRequestEvent.ID, setup.PublishResponse)

			require.True(t, setup.publishCalled)
			require.Nil(t, setup.response.Error, "message %q must still be signable", message)
			result, ok := setup.response.Result.(signMessageResponse)
			require.True(t, ok, "expected a signMessageResponse, got %T", setup.response.Result)
			assert.Equal(t, message, result.Message)
			assert.NotEmpty(t, result.Signature)
		})
	}
}
