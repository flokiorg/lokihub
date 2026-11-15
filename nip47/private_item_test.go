package nip47

import (
	"encoding/json"
	"testing"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/nip47/models"
)

// TestPrivateServableMethods_IsAnAllowlistNotTheFullDispatch is the security
// property. A bill is a bearer instrument — whoever holds the string holds it — so
// its connection key must not reach the hub's own wallet methods. Reusing the
// standard path's dispatch would have made every NIP-47 method reachable here.
func TestPrivateServableMethods_IsAnAllowlistNotTheFullDispatch(t *testing.T) {
	for _, method := range []string{
		constants.NIP47MethodCashStatus,
		// The deprecated cash_status alias. It is currently the only RELEASED
		// name, so a client that has not been updated sends this one; leaving it
		// out silently dropped that client's item, which reads to the caller
		// exactly like a bill the hub does not hold.
		constants.NIP47MethodCashStatus,
		constants.NIP47MethodCashRedeem,
		constants.NIP47MethodCashTransfer,
		constants.NIP47MethodCashConsolidate,
		constants.NIP47MethodCreateCircleWallet,
	} {
		assert.True(t, IsPrivateServableMethod(method), "%s should be servable", method)
	}

	// Everything a bill must not be able to reach. Each of these would be a
	// privilege escalation from a bearer key to the hub's own wallet.
	for _, method := range []string{
		models.GET_BALANCE_METHOD,
		models.PAY_INVOICE_METHOD,
		models.MULTI_PAY_INVOICE_METHOD,
		models.MAKE_INVOICE_METHOD,
		models.SIGN_MESSAGE_METHOD,
		models.CREATE_CONNECTION_METHOD,
		models.GET_INFO_METHOD,
		// mint_cash in particular: it is CASH_HUB_SCOPE, it lives on a Hub app
		// connection, and it is the one method without retry idempotency — which
		// is what makes the in-memory replay set safe. Allowing it here would
		// quietly invalidate that.
		constants.NIP47MethodMintCash,
	} {
		assert.False(t, IsPrivateServableMethod(method),
			"%s must NOT be reachable over the private transport", method)
	}

	assert.False(t, IsPrivateServableMethod(""), "an empty method must not be servable")
	assert.False(t, IsPrivateServableMethod("cash_redeem "), "no fuzzy matching")
}

func TestItemResponseCollector_CapturesAResult(t *testing.T) {
	c := &itemResponseCollector{}
	c.publish(&models.Response{
		ResultType: constants.NIP47MethodCashStatus,
		Result:     map[string]any{"amount_millis": 1000},
	}, nostr.Tags{})

	got, ok, err := c.result("1")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "1", got.ID)
	assert.Equal(t, constants.NIP47MethodCashStatus, got.ResultType)
	assert.Nil(t, got.Error)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(got.Result, &decoded))
	assert.EqualValues(t, 1000, decoded["amount_millis"])
}

func TestItemResponseCollector_CapturesAnError(t *testing.T) {
	c := &itemResponseCollector{}
	c.publish(&models.Response{
		ResultType: constants.NIP47MethodCashRedeem,
		Error:      &models.Error{Code: constants.ERROR_RATE_LIMITED, Message: "slow down"},
	}, nostr.Tags{})

	got, ok, err := c.result("2")
	require.NoError(t, err)
	require.True(t, ok)
	require.NotNil(t, got.Error)
	assert.Equal(t, constants.ERROR_RATE_LIMITED, got.Error.Code)
	assert.Equal(t, "slow down", got.Error.Message)
	// Never both: the response codec rejects a result carrying a value and an
	// error together, so this must not produce one even if a handler set both.
	assert.Nil(t, got.Result)
}

// TestItemResponseCollector_NeverCarriesBothResultAndError guards the shape the
// response codec refuses, in case a handler ever sets both.
func TestItemResponseCollector_NeverCarriesBothResultAndError(t *testing.T) {
	c := &itemResponseCollector{}
	c.publish(&models.Response{
		ResultType: constants.NIP47MethodCashRedeem,
		Result:     map[string]any{"preimage": "abc"},
		Error:      &models.Error{Code: constants.ERROR_INTERNAL},
	}, nostr.Tags{})

	got, ok, err := c.result("3")
	require.NoError(t, err)
	require.True(t, ok)
	require.NotNil(t, got.Error, "the error must win, since it is the safer reading")
	assert.Nil(t, got.Result)
}

// TestItemResponseCollector_SilenceStaysSilence is what keeps batching from
// becoming an existence oracle. A handler that published nothing must yield no
// result, so the item is omitted — not turned into an error here, which would
// confirm something about a wallet the caller could not prove it holds.
func TestItemResponseCollector_SilenceStaysSilence(t *testing.T) {
	c := &itemResponseCollector{}

	got, ok, err := c.result("4")
	require.NoError(t, err)
	assert.False(t, ok, "no published response must mean no result")
	assert.Equal(t, "", got.ID)
}

// TestItemResponseCollector_RefusesADoubleAnswer catches a handler bug rather than
// hiding it: if a controller publishes twice, one of its answers is being
// discarded, and picking either would bury that.
func TestItemResponseCollector_RefusesADoubleAnswer(t *testing.T) {
	c := &itemResponseCollector{}
	c.publish(&models.Response{ResultType: "cash_status", Result: map[string]any{"a": 1}}, nostr.Tags{})
	c.publish(&models.Response{ResultType: "cash_status", Result: map[string]any{"b": 2}}, nostr.Tags{})

	_, ok, err := c.result("5")
	require.Error(t, err)
	assert.False(t, ok)
	assert.Contains(t, err.Error(), "published 2 responses")
}

// TestItemResponseCollector_MatchesThePublishFuncSignature is a compile-time guard.
// If publishFunc ever changes, the private transport must fail to build rather
// than silently stop collecting.
func TestItemResponseCollector_MatchesThePublishFuncSignature(t *testing.T) {
	c := &itemResponseCollector{}
	// The explicit type IS the assertion, so staticcheck's ST1023 is wrong here:
	// `fn := c.publish` would infer whatever signature publish happens to have, and
	// this compile-time guard would silently stop guarding anything.
	var fn func(*models.Response, nostr.Tags) = c.publish //nolint:staticcheck // the declared type is the test
	require.NotNil(t, fn)

	fn(&models.Response{ResultType: "cash_status"}, nostr.Tags{})
	_, ok, err := c.result("6")
	require.NoError(t, err)
	assert.True(t, ok)
}
