package decodepay

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClampIntToUint64(t *testing.T) {
	assert.Equal(t, uint64(0), clampIntToUint64(0))
	assert.Equal(t, uint64(1000), clampIntToUint64(1000))
	assert.Equal(t, uint64(0), clampIntToUint64(-1))
	assert.Equal(t, uint64(0), clampIntToUint64(math.MinInt))
	assert.Equal(t, uint64(math.MaxInt), clampIntToUint64(math.MaxInt))
}

func TestClampIntToUint32(t *testing.T) {
	assert.Equal(t, uint32(0), clampIntToUint32(0))
	assert.Equal(t, uint32(1000), clampIntToUint32(1000))
	assert.Equal(t, uint32(0), clampIntToUint32(-1))
	assert.Equal(t, uint32(0), clampIntToUint32(math.MinInt))
	assert.Equal(t, uint32(math.MaxUint32), clampIntToUint32(math.MaxUint32))
	assert.Equal(t, uint32(math.MaxUint32), clampIntToUint32(math.MaxUint32+1))
	assert.Equal(t, uint32(math.MaxUint32), clampIntToUint32(math.MaxInt))
}

// A second 32-byte payment hash field refuses the invoice before anything else
// reads it; a p field of another length is skipped, as BOLT 11 says.
func TestDecode_RepeatedPaymentHashIsRefused(t *testing.T) {
	for name, inv := range map[string]string{
		"two payment hashes are refused":                                  "lnfc1u1qqqqqqqpp5424242424242424242424242424242424242424242424242424qpp5hwamhwamhwamhwamhwamhwamhwamhwamhwamhwamhwamhwamhwasqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq2ygrr0",
		"the same payment hash twice is refused":                          "lnfc1u1qqqqqqqpp5424242424242424242424242424242424242424242424242424qpp5424242424242424242424242424242424242424242424242424qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqyycxat",
		"a payment hash with non-zero padding then a good one is refused": "lnfc1u1qqqqqqqpp5424242424242424242424242424242424242424242424242424ppp5424242424242424242424242424242424242424242424242424qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqcljde6",
	} {
		_, err := Decode(inv)
		assert.ErrorIs(t, err, ErrRepeatedPaymentHash, name)
	}
	assert.Equal(t, 1, paymentHashFields("lnfc1u1qqqqqqqppj4242424242424242424242424242424242424242424242424gpp5424242424242424242424242424242424242424242424242424qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqymzyf9"), "a short p field then a hash-length one")
}

func TestDecode_OnePaymentHashDecodes(t *testing.T) {
	const invoice = "lntbs1230n1pnkqautdqyw3jsnp4q09a0z84kg4a2m38zjllw43h953fx5zvqe8qxfgw694ymkq26u8zcpp5yvnh6hsnlnj4xnuh2trzlnunx732dv8ta2wjr75pdfxf6p2vlyassp5hyeg97a3ft5u769kjwsn7p0e85h79pzz8kladmnqhpcypz2uawjs9qyysgqcqpcxq8zals8sq9yeg2pa9eywkgj50cyzxd5elatujuc0c0wh6j9nat5mn34pgk8u9ufpgs99tw9ldlfk42cqlkr48au3lmuh09269prg4qkggh4a8cyqpfl0y6j"
	assert.Equal(t, 1, paymentHashFields(invoice))
	inv, err := Decode(invoice)
	require.NoError(t, err)
	assert.Len(t, inv.PaymentHash, 64)
}
