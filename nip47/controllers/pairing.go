package controllers

import (
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/nip47/cipher"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/nbd-wtf/go-nostr"
)

// respondError builds and publishes a NIP-47 error response for method. Shared
// by the Cash/circle wallet controllers, which otherwise each repeat this same
// five-line block for every one of their many validation branches.
func respondError(publishResponse publishFunc, method, code, message string) {
	publishResponse(&models.Response{
		ResultType: method,
		Error:      &models.Error{Code: code, Message: message},
	}, nostr.Tags{})
}

// encryptForRecipient NIP-44 encrypts plaintext for the recipient: a circle
// wallet's encrypted_details JSON, or a cash token handed over by
// cash_transfer/cash_consolidate. recipientPubkey is the nostr public key
// (hex); senderPrivKey is the wallet's private key used to derive the shared
// secret.
func encryptForRecipient(recipientPubkey, senderPrivKey, plaintext string) (string, error) {
	c, err := cipher.NewNip47Cipher(constants.ENCRYPTION_TYPE_NIP44_V2, recipientPubkey, senderPrivKey)
	if err != nil {
		return "", err
	}
	return c.Encrypt(plaintext)
}
