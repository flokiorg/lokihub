package queries

import "gorm.io/gorm"

// GetCircleWalletMembershipPubkeysByWalletAppIDs returns each circle_wallet
// child's member identity pubkey (CircleWalletMembership.RequesterPubkey),
// keyed by WalletAppID, for every given wallet app ID in a single query. A
// wallet with no membership row (shouldn't normally happen) has no entry in
// the result.
func GetCircleWalletMembershipPubkeysByWalletAppIDs(tx *gorm.DB, walletAppIDs []uint) (map[uint]string, error) {
	if len(walletAppIDs) == 0 {
		return map[uint]string{}, nil
	}

	var rows []struct {
		WalletAppID     uint
		RequesterPubkey string
	}
	err := tx.Table("circle_wallet_memberships").
		Select("wallet_app_id, requester_pubkey").
		Where("wallet_app_id IN ?", walletAppIDs).
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}

	pubkeys := make(map[uint]string, len(rows))
	for _, r := range rows {
		pubkeys[r.WalletAppID] = r.RequesterPubkey
	}
	return pubkeys, nil
}
