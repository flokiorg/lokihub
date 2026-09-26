//go:build integration

package integration

import (
	"encoding/json"
	"fmt"

	"github.com/flokiorg/lokihub/integration/nwcclient"
)

// DecryptCircleWalletDetails opens a create_circle_wallet response the way the
// joining member does: NIP-44 to the member's own identity key, from the circle
// hub wallet's pubkey. No other holder of the shared circlehub connection can
// do this, which is the point — see CircleWalletDetails.
func DecryptCircleWalletDetails(memberPrivkey, hubWalletPubkey string, result CreateCircleWalletResult) (CircleWalletDetails, error) {
	var details CircleWalletDetails
	plaintext, err := nwcclient.DecryptPayload(memberPrivkey, hubWalletPubkey, result.EncryptedDetails)
	if err != nil {
		return details, fmt.Errorf("decrypt circle wallet details: %w", err)
	}
	if err := json.Unmarshal([]byte(plaintext), &details); err != nil {
		return details, fmt.Errorf("unmarshal circle wallet details: %w", err)
	}
	return details, nil
}
