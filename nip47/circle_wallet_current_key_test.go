package nip47

import (
	"testing"
	"time"

	"github.com/ohstr/nmilat/nipcw"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/nip47/models"
)

// privateOnly are methods the standard transport refuses NOT_IMPLEMENTED.
var privateOnly = []string{constants.NIP47MethodCashRedeem, constants.NIP47MethodCreateCircleWallet,
	constants.NIP47MethodCashOrder, constants.NIP47MethodCashLookup}

func pendingRotations(t *testing.T, r *rotationCircle) int64 {
	t.Helper()
	var n int64
	require.NoError(t, r.svc.DB.Model(&db.CircleRotation{}).Where("state = ?", db.CircleRotationPending).Count(&n).Error)
	return n
}

// NIP-CW §What a Circle Wallet Connection Allows: a frozen wallet's current
// key is refused every request with FROZEN, a private-only or unknown method
// included; unfrozen, those get their usual answers.
func TestFrozenWallet_EveryMethodFrozen(t *testing.T) {
	r := newRotationCircle(t)
	require.Empty(t, r.submit(t, r.request(t, r.memberPriv, nipcw.ActionFreeze, ""), time.Now()).Code)
	for _, method := range append([]string{"no_such_method", models.GET_BALANCE_METHOD}, privateOnly...) {
		assert.Equal(t, nipcw.ErrFrozen, errorCode(r.w.call(method, map[string]interface{}{})), method)
	}

	require.Empty(t, r.submit(t, r.request(t, r.memberPriv, nipcw.ActionUnfreeze, ""), time.Now()).Code)
	for _, method := range privateOnly {
		assert.Equal(t, constants.ERROR_NOT_IMPLEMENTED, errorCode(r.w.call(method, map[string]interface{}{})), method)
	}
	balance := r.w.call(models.GET_BALANCE_METHOD, nil)
	assert.Nil(t, balance.Error, "%+v", balance.Error)
}

// NIP-CW §Recovering a Lost Connection: any request validly signed by the old
// key cancels a pending rotation, a private-only method's included, and is
// then answered as usual.
func TestRotation_AnyOldKeyRequestCancels(t *testing.T) {
	for _, method := range privateOnly {
		t.Run(method, func(t *testing.T) {
			r := newRotationCircle(t)
			newKey, _ := nipcw.NewConnectionKey()
			require.Empty(t, r.submit(t, r.request(t, r.memberPriv, nipcw.ActionRotate, newKey.Pubkey()), time.Now()).Code)
			require.EqualValues(t, 1, pendingRotations(t, r))
			assert.Equal(t, constants.ERROR_NOT_IMPLEMENTED, errorCode(r.w.call(method, map[string]interface{}{})))
			assert.Zero(t, pendingRotations(t, r), "the old key's request cancelled the rotation")
		})
	}
}

// A frozen wallet's old key cancels nothing: it is refused FROZEN and the
// rotation stays pending.
func TestRotation_FrozenOldKeyCancelsNothing(t *testing.T) {
	r := newRotationCircle(t)
	newKey, _ := nipcw.NewConnectionKey()
	require.Empty(t, r.submit(t, r.request(t, r.memberPriv, nipcw.ActionRotate, newKey.Pubkey()), time.Now()).Code)
	require.Empty(t, r.submit(t, r.request(t, r.memberPriv, nipcw.ActionFreeze, ""), time.Now()).Code)
	for _, method := range []string{constants.NIP47MethodCashRedeem, models.GET_BALANCE_METHOD} {
		assert.Equal(t, nipcw.ErrFrozen, errorCode(r.w.call(method, map[string]interface{}{})), method)
	}
	assert.EqualValues(t, 1, pendingRotations(t, r))
}
