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
	"github.com/flokiorg/lokihub/lokicash"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/flokiorg/lokihub/tests"
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

// TestSignMessage_RefusesMintProvenancePayload is the security property: an app
// holding the sign_message scope must not be able to borrow the hub's node key
// to produce a mint-provenance signature. The hub's node key is the same key
// lokicash.VerifyMint recovers a minter from, so a signature over a
// MintPayload string is indistinguishable from real provenance.
func TestSignMessage_RefusesMintProvenancePayload(t *testing.T) {
	setup := setupSignMessageTest(t)
	defer setup.TearDown()

	payload := lokicash.MintPayload(lokicash.HRP, forgeryVictimWalletPubkey, forgeryInflatedAmount)
	req := signMessageRequest(t, payload)

	NewTestNip47Controller(setup.svc).
		HandleSignMessageEvent(setup.ctx, req, setup.dbRequestEvent.ID, setup.PublishResponse)

	require.True(t, setup.publishCalled)
	require.NotNil(t, setup.response.Error, "signing a mint payload must be refused")
	assert.Equal(t, constants.ERROR_RESTRICTED, setup.response.Error.Code)
	// No signature may leak, not even alongside an error.
	assert.Nil(t, setup.response.Result)
}

// TestSignMessage_RefusesMintProvenancePayloadAnyCase pins that the refusal does
// not hinge on the payload's casing.
func TestSignMessage_RefusesMintProvenancePayloadAnyCase(t *testing.T) {
	payload := lokicash.MintPayload(lokicash.HRP, forgeryVictimWalletPubkey, forgeryInflatedAmount)

	for name, variant := range map[string]string{
		"upper":      strings.ToUpper(payload),
		"mixedcase":  strings.Replace(payload, "lokicash-mint:", "LokiCash-Mint:", 1),
		"prefixOnly": lokicash.MintPayloadPrefix,
	} {
		t.Run(name, func(t *testing.T) {
			setup := setupSignMessageTest(t)
			defer setup.TearDown()

			NewTestNip47Controller(setup.svc).
				HandleSignMessageEvent(setup.ctx, signMessageRequest(t, variant),
					setup.dbRequestEvent.ID, setup.PublishResponse)

			require.True(t, setup.publishCalled)
			require.NotNil(t, setup.response.Error, "variant %q must be refused", variant)
			assert.Equal(t, constants.ERROR_RESTRICTED, setup.response.Error.Code)
		})
	}
}

// TestSignMessage_MintPayloadIsGenuinelyForgeable is what keeps the test above
// from going vacuous. It proves the refused payload really is a forgery vector:
// signed through the very same node primitive, it yields a token whose
// provenance verifies as this hub's own, for a wallet pubkey the signer never
// minted and an amount it never attested.
//
// If MintPayload's shape or the provenance scheme ever changes such that this
// no longer forges, this test fails and the guard above should be re-derived
// rather than trusted.
func TestSignMessage_MintPayloadIsGenuinelyForgeable(t *testing.T) {
	setup := setupSignMessageTest(t)
	defer setup.TearDown()

	payload := lokicash.MintPayload(lokicash.HRP, forgeryVictimWalletPubkey, forgeryInflatedAmount)

	// Bypass the controller and hit the node primitive directly — this is what
	// the guard is denying access to.
	zsig, err := setup.svc.LNClient.SignMessage(setup.ctx, payload)
	require.NoError(t, err)
	raw, err := zbase32.DecodeString(zsig)
	require.NoError(t, err)
	require.Len(t, raw, 65, "a node signature is a 65-byte recoverable compact sig")

	amount := forgeryInflatedAmount
	forged := lokicash.Token{
		HRP:            lokicash.HRP,
		WalletPubkey:   forgeryVictimWalletPubkey,
		MintSignature:  raw,
		AttestedAmount: &amount,
	}

	recovered, ok := lokicash.VerifyMint(forged)
	require.True(t, ok, "the forged token must verify — otherwise the guard defends nothing")
	assert.Equal(t, setup.hubNodePubkey, recovered,
		"forged provenance recovers to this hub's node key, which is exactly the attack")
}

// TestSignMessage_AllowsOrdinaryMessage guards against over-blocking: the method
// must keep working for everything that is not a mint payload.
func TestSignMessage_AllowsOrdinaryMessage(t *testing.T) {
	for name, message := range map[string]string{
		"plain":              "hello world",
		"mentionsLokicash":   "paying you in lokicash today",
		"prefixNotAtStart":   "see also lokicash-mint:v1:lokicash:deadbeef:1",
		"similarButDistinct": "lokicash-minted:v1:lokicash:deadbeef:1",
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
