package cashwallet

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/flokiorg/lokihub/db"
)

// A regenerated Cash Hub connection keeps the hub group input (WP-25 item 7;
// NIP-CASH §The Hub Group Fingerprint: never changed once it minted).
func TestHubGroupInput_KeptAfterRegenerate(t *testing.T) {
	first := "aa"
	assert.Equal(t, "bb", hubGroupInput(&db.App{AppPubkey: "bb"}))
	assert.Equal(t, "aa", hubGroupInput(&db.App{AppPubkey: "bb", HubGroupPubkey: &first}))
}
