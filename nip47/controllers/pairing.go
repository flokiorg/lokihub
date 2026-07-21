package controllers

import (
	"strings"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/nip47/cipher"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/nbd-wtf/go-nostr"
)

// respondError builds and publishes a NIP-47 error response for method. Shared
// by the JIT/circle wallet controllers, which otherwise each repeat this same
// five-line block for every one of their many validation branches.
func respondError(publishResponse publishFunc, method, code, message string) {
	publishResponse(&models.Response{
		ResultType: method,
		Error:      &models.Error{Code: code, Message: message},
	}, nostr.Tags{})
}

// buildNWCPairingURI assembles the nostr+walletconnect pairing URI.
// Uses strings.Builder to avoid intermediate allocations from fmt.Sprintf.
func buildNWCPairingURI(walletPubkey string, relayUrls []string, secret string) string {
	var b strings.Builder
	b.WriteString("nostr+walletconnect://")
	b.WriteString(walletPubkey)
	b.WriteString("?relay=")
	b.WriteString(strings.Join(relayUrls, "&relay="))
	b.WriteString("&secret=")
	b.WriteString(secret)
	return b.String()
}

// encryptPairingURI NIP-44 encrypts the pairing URI for the recipient.
// recipientPubkey is the nostr public key (hex); senderPrivKey is the wallet's
// private key used to derive the shared secret.
func encryptPairingURI(recipientPubkey, senderPrivKey, uri string) (string, error) {
	c, err := cipher.NewNip47Cipher(constants.ENCRYPTION_TYPE_NIP44_V2, recipientPubkey, senderPrivKey)
	if err != nil {
		return "", err
	}
	return c.Encrypt(uri)
}
