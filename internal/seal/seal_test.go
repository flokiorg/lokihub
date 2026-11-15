package seal_test

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/internal/seal"
)

func TestRoundTrip(t *testing.T) {
	key := bytes.Repeat([]byte{7}, seal.KeyLen)
	sealed, err := seal.EncryptWithKey("secret", key)
	require.NoError(t, err)
	got, err := seal.DecryptWithKey(sealed, key)
	require.NoError(t, err)
	assert.Equal(t, "secret", got)

	other := bytes.Repeat([]byte{8}, seal.KeyLen)
	_, err = seal.DecryptWithKey(sealed, other)
	assert.Error(t, err)
}

func TestRejectsBadKeyLength(t *testing.T) {
	_, err := seal.EncryptWithKey("x", []byte("short"))
	assert.Error(t, err)
	_, err = seal.DecryptWithKey("aa-bb", []byte("short"))
	assert.Error(t, err)
}

func TestMalformedSealedValueIsAnErrorNotAPanic(t *testing.T) {
	key := bytes.Repeat([]byte{7}, seal.KeyLen)
	for _, in := range []string{"", "nodash", "a-b-c", "zz-zz", "00-00"} {
		_, err := seal.DecryptWithKey(in, key)
		assert.Error(t, err, in)
	}
}
