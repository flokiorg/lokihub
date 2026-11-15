package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/ohstr/nmilat/nipcash"

	"github.com/flokiorg/lokihub/logger"
	"github.com/flokiorg/lokihub/nodesign"
)

// checkCashIdentityNetwork records the network the node identity signs cash on,
// the first time, and refuses one recorded under another network (NIP-CASH
// §Mint Provenance; §Connection Signatures). nodesign.Guard runs it before the
// node identity signs anything cash.
func (svc *service) checkCashIdentityNetwork(ctx context.Context) error {
	ln := svc.lnClient
	if ln == nil {
		return errors.New("the node is not started")
	}
	info, err := ln.GetInfo(ctx)
	if err != nil {
		return fmt.Errorf("could not read the node's network: %w", err)
	}
	identity, err := nipcash.MinterXOnly(info.Pubkey)
	if err != nil {
		return err
	}
	recorded, err := svc.cfg.Get(nodesign.IdentityNetworksKey, "")
	if err != nil {
		return fmt.Errorf("could not read %s: %w", nodesign.IdentityNetworksKey, err)
	}
	next, err := nodesign.CheckIdentityNetwork(recorded, identity, info.Network)
	if err != nil {
		logger.Logger.Error().Err(err).Str("node_identity", identity[:8]).Msg("Refusing cash serving on this node identity")
		return err
	}
	if next != "" {
		if err := svc.cfg.SetUpdate(nodesign.IdentityNetworksKey, next, ""); err != nil {
			return fmt.Errorf("could not record the node identity's network: %w", err)
		}
	}
	return nil
}
