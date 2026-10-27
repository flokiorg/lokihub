// Package connstr issues the signed connection strings a Hub hands out
// (NIP-CASH §The Cash Hub Connection, §Connection Signatures). A Hub that cannot
// sign issues no string.
package connstr

import (
	"context"
	"fmt"

	"github.com/ohstr/nmilat/nipcash"

	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/nodesign"
)

// CashHub is a signed cashhub1 string for a Cash Hub connection: its pubkey
// (TLV 0), relay hints, secret and label, signed by the node identity (TLV 4/5)
// under chain nodesign.Chain (TLV 6).
func CashHub(ctx context.Context, ln lnclient.LNClient, pubkey string, relays []string, secret, label string) (string, error) {
	identity, err := nodesign.XOnly(ctx, ln)
	if err != nil {
		return "", err
	}
	c := nipcash.CashHubConnection{
		Pubkey:      pubkey,
		Relays:      relays,
		Secret:      secret,
		Label:       label,
		HasLabel:    label != "",
		HubIdentity: identity,
		Chain:       nodesign.Chain,
	}
	signed, err := nipcash.SignCashHubConnection(c, nodesign.Signer(ctx, ln), identity)
	if err != nil {
		return "", fmt.Errorf("could not sign the cash hub connection: %w", err)
	}
	return nipcash.EncodeCashHubConnection(signed)
}
