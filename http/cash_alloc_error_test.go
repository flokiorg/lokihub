package http

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/flokiorg/lokihub/cashrequest"
)

// An admin mint that reuses a request_id is a conflict the operator looks up,
// and a commit past its deadline is the caller's error; neither is a 500.
func TestMapCashAllocError_RequestIDOutcomes(t *testing.T) {
	code, msg := mapCashAllocError(fmt.Errorf("mint: %w", cashrequest.ErrRequestReused))
	assert.Equal(t, http.StatusConflict, code)
	assert.Contains(t, msg, "REQUEST_REUSED")

	code, msg = mapCashAllocError(fmt.Errorf("mint: %w", cashrequest.ErrDeadlinePassed))
	assert.Equal(t, http.StatusBadRequest, code)
	assert.Contains(t, msg, "deadline")
}
