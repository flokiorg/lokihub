package lnd

import (
	"encoding/hex"
	"fmt"

	"github.com/flokiorg/flnd/lnrpc"
	"google.golang.org/protobuf/proto"
	"gopkg.in/macaroon-bakery.v2/bakery"
	"gopkg.in/macaroon.v2"
)

// SignMessageURI is the full gRPC method the announcement signer calls.
const SignMessageURI = "/signrpc.Signer/SignMessage"

// MacaroonGrants reports whether the hex-encoded flnd macaroon carries the
// permission entity:action, either directly (as admin.macaroon carries
// signer:generate) or as a URI permission for fullMethod (a macaroon baked
// with `flncli bakemacaroon uri:/signrpc.Signer/SignMessage`).
//
// It reads the permissions flnd encodes in the macaroon id, the same way
// `flncli printmacaroon` does. It checks no caveats (timeout, IP lock):
// flnd itself still decides at call time.
func MacaroonGrants(macaroonHex, entity, action, fullMethod string) (bool, error) {
	macBytes, err := hex.DecodeString(macaroonHex)
	if err != nil {
		return false, fmt.Errorf("decoding macaroon hex: %w", err)
	}
	mac := &macaroon.Macaroon{}
	if err := mac.UnmarshalBinary(macBytes); err != nil {
		return false, fmt.Errorf("decoding macaroon: %w", err)
	}
	rawID := mac.Id()
	if len(rawID) == 0 || rawID[0] != byte(bakery.LatestVersion) {
		return false, fmt.Errorf("unsupported macaroon id version")
	}
	id := &lnrpc.MacaroonId{}
	if err := proto.Unmarshal(rawID[1:], id); err != nil {
		return false, fmt.Errorf("decoding macaroon id: %w", err)
	}

	for _, op := range id.Ops {
		for _, a := range op.Actions {
			if op.Entity == entity && a == action {
				return true, nil
			}
			if fullMethod != "" && op.Entity == "uri" && a == fullMethod {
				return true, nil
			}
		}
	}
	return false, nil
}

// CheckSignerMacaroon returns an error unless the node's configured
// macaroon can call signrpc SignMessage (signer:generate), which signing a
// NIP-LSP announcement with the node key needs (T8-1).
func CheckSignerMacaroon(macaroonHex string) error {
	ok, err := MacaroonGrants(macaroonHex, "signer", "generate", SignMessageURI)
	if err != nil {
		return fmt.Errorf("checking flnd.macaroon for signer:generate: %w", err)
	}
	if !ok {
		return fmt.Errorf("flnd.macaroon cannot call %s, which announcing on Nostr needs to sign with the node key; "+
			"bake one with `flncli bakemacaroon uri:%s ...` plus the permissions flspd already uses "+
			"(narrower than signer:generate, which admin.macaroon carries)", SignMessageURI, SignMessageURI)
	}
	return nil
}
