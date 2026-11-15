package service

import (
	"fmt"

	"github.com/nbd-wtf/go-nostr"
)

// appWalletKeys derives the wallet keypair of app id: the key the app's NWC
// requests are addressed to and its replies are signed with.
func (svc *service) appWalletKeys(id uint) (priv, pub string, err error) {
	priv, err = svc.keys.GetAppWalletKey(id)
	if err != nil {
		return "", "", fmt.Errorf("calculate app wallet priv key: %w", err)
	}
	pub, err = nostr.GetPublicKey(priv)
	if err != nil {
		return "", "", fmt.Errorf("calculate app wallet pub key: %w", err)
	}
	return priv, pub, nil
}
