package listing

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/ohstr/nmilat/nip01"

	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/nodesign"
)

// LNSigner is NodeSigner over the node: BIP340 by the node key through
// lnclient.TransportSigner, the signer of the kind-11190 announcement.
type LNSigner struct {
	// LN returns the running node's client (nil while it is not started).
	LN func() lnclient.LNClient
}

// Identity is the node identity, x-only.
func (s LNSigner) Identity(ctx context.Context) (string, error) {
	return nodesign.XOnly(ctx, s.LN())
}

// Network is the node's network as get_info reports it.
func (s LNSigner) Network(ctx context.Context) (string, error) {
	ln := s.LN()
	if ln == nil {
		return "", errors.New("the node is not started")
	}
	info, err := ln.GetInfo(ctx)
	if err != nil {
		return "", err
	}
	return nodesign.Network(info.Network), nil
}

// Sign hashes and signs ev with the node key, then verifies it.
func (s LNSigner) Sign(ctx context.Context, ev *nip01.Event) error {
	signer, ok := s.LN().(lnclient.TransportSigner)
	if !ok {
		return errors.New("this LN backend cannot sign a node event (needs BIP340 over the node key)")
	}
	id, err := ev.HashID()
	if err != nil {
		return fmt.Errorf("hash: %w", err)
	}
	ev.ID = hex.EncodeToString(id)
	// The node hashes what it is given: hand it the serialization, not the id.
	payload, err := ev.Serialize()
	if err != nil {
		return fmt.Errorf("serialize: %w", err)
	}
	sig, err := signer.SignSchnorrNodeKey(ctx, payload)
	if err != nil {
		return fmt.Errorf("the node refused to sign: %w", err)
	}
	ev.Sig = hex.EncodeToString(sig)
	if err := ev.Verify(nip01.WithoutPowCheck()); err != nil {
		return fmt.Errorf("the signed event does not verify: %w", err)
	}
	return nil
}

// KeySigner is NodeSigner over a private key, for tests and the KATs
// (LocalChainSigner's role).
type KeySigner struct {
	Priv    string
	Pubkey  string
	Netw    string
	SignErr error
}

// Identity is the key's x-only pubkey.
func (k KeySigner) Identity(context.Context) (string, error) { return k.Pubkey, nil }

// Network is the configured network.
func (k KeySigner) Network(context.Context) (string, error) { return k.Netw, nil }

// Sign signs with the key.
func (k KeySigner) Sign(_ context.Context, ev *nip01.Event) error {
	if k.SignErr != nil {
		return k.SignErr
	}
	return ev.Sign(k.Priv)
}
