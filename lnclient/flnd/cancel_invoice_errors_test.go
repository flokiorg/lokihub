package flnd

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestCancelInvoiceErrors_UnknownAndAlreadyCancelledAreRecognised(t *testing.T) {
	assert.True(t, invoiceUnknown(status.Error(codes.NotFound, "no such invoice")))
	assert.True(t, invoiceUnknown(status.Error(codes.Unknown, "unable to locate invoice")))
	assert.False(t, invoiceUnknown(status.Error(codes.Unavailable, "connection refused")))
	assert.False(t, invoiceUnknown(errors.New("invoice already settled")))

	assert.True(t, cancelledAlready(status.Error(codes.Unknown, "invoice already canceled")))
	assert.False(t, cancelledAlready(status.Error(codes.Unknown, "invoice already settled")))
}
