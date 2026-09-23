package apps

import (
	"fmt"
	"time"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
)

// CashClaimFilter selects a page of a hub's cash slices, live and archived
// together.
type CashClaimFilter struct {
	HubID uint
	// Status is a db.CashSliceStatus* value, "" for unfiltered, or the legacy
	// "claimed" which maps to redeemed+split (see cashStatusFilterValues).
	Status string
	// Limit 0 returns every row unpaginated — integration/admin_client.go
	// relies on ?limit=0.
	Limit  uint64
	Offset uint64
	// Now decides whether an unclaimed live slice reads as expired. Passed in
	// rather than taken from the clock so a caller's counts and page agree.
	Now time.Time
}

// CashClaimRow is one row of the merged listing. Flat rather than embedding
// db.CashWalletClaim, because an archived row is not one — it comes from
// cash_bill_slice_archives, whose bill no longer exists.
type CashClaimRow struct {
	// Archived distinguishes the two sources. Load-bearing, not cosmetic: live
	// claim ids and archive ids come from different sequences and WILL
	// collide, so any caller keying on ID alone (a dedupe map, a delete URL)
	// must pair it with this.
	Archived bool

	ID                   uint
	WalletAppID          uint
	IdentityType         string
	IdentityValue        string
	IAPubkey             string
	AmountMloki          int64
	ClaimedAt            *time.Time
	CreatedAt            time.Time
	MinTransferMloki     int64
	RedeemFeePpm         int
	SpunOffToWalletAppID *uint
	WalletExpiresAt      *time.Time
	WalletPubkey         string
	// Status is a db.CashSliceStatus* value: derived in SQL for a live row,
	// read straight off the stored column for an archived one.
	Status string

	PaymentHash     string
	Preimage        string
	RedeemFeeMloki  int64
	RoutingFeeMloki int64
	SettledAt       *time.Time
}

// cashClaimUnionSQL is the merged row source: live claims and archived slices
// projected into one shape.
//
// UNION ALL inside a derived table, rather than two queries merged in Go,
// because a merged sort cannot be paginated without over-fetching both sides —
// and over-fetching is exactly what this replaced. It is also not a SQL view:
// that would be a schema object to migrate on two dialects.
//
// Two dialect traps are deliberately avoided. Every column is a real column of
// a matching type on both branches, since a bare NULL types as "unknown" on
// Postgres and is rejected in a UNION. And the archived flag is an integer 0/1,
// not TRUE/FALSE, because sqlite has no boolean type.
//
// Placeholder order: now, hubID (live branch), then hubID (archived branch).
const cashClaimUnionSQL = `
SELECT
  0 AS archived,
  c.id AS id,
  c.wallet_app_id AS wallet_app_id,
  c.identity_type AS identity_type,
  c.identity_value AS identity_value,
  c.ia_pubkey AS ia_pubkey,
  c.amount_mloki AS amount_mloki,
  c.claimed_at AS claimed_at,
  c.created_at AS created_at,
  c.min_transfer_mloki AS min_transfer_mloki,
  c.redeem_fee_ppm AS redeem_fee_ppm,
  c.spun_off_to_wallet_app_id AS spun_off_to_wallet_app_id,
  a.expires_at AS wallet_expires_at,
  COALESCE(a.wallet_pubkey, '') AS wallet_pubkey,
  CASE
    WHEN c.claimed_at IS NOT NULL AND c.spun_off_to_wallet_app_id IS NOT NULL THEN 'split'
    WHEN c.claimed_at IS NOT NULL THEN 'redeemed'
    WHEN a.expires_at IS NOT NULL AND a.expires_at < ? THEN 'expired'
    ELSE 'unclaimed'
  END AS status,
  c.payment_hash AS payment_hash,
  c.preimage AS preimage,
  c.redeem_fee_mloki AS redeem_fee_mloki,
  c.routing_fee_mloki AS routing_fee_mloki,
  c.settled_at AS settled_at
FROM cash_wallet_claims c
JOIN apps a ON a.id = c.wallet_app_id
WHERE a.parent_app_id = ? AND a.parent_kind = 'cash' AND a.kind = 'cash_wallet'

UNION ALL

SELECT
  1, s.id, s.wallet_app_id,
  s.identity_type, s.identity_value, s.ia_pubkey, s.amount_mloki,
  s.claimed_at, s.created_at,
  s.min_transfer_mloki, s.redeem_fee_ppm, s.spun_off_to_wallet_app_id,
  s.wallet_expires_at, COALESCE(s.wallet_pubkey, ''),
  s.outcome,
  s.payment_hash, s.preimage, s.redeem_fee_mloki, s.routing_fee_mloki, s.settled_at
FROM cash_bill_slice_archives s
WHERE s.hub_app_id = ? AND s.outcome <> 'void'
`

// cashStatusFilterValues maps a requested status onto the values it matches.
// "claimed" is the legacy value, from before the vocabulary distinguished how
// a slice's value left; it still works, and still means "not unclaimed".
func cashStatusFilterValues(status string) ([]string, error) {
	switch status {
	case "":
		return nil, nil
	case db.CashSliceStatusUnclaimed, db.CashSliceStatusRedeemed, db.CashSliceStatusSplit,
		db.CashSliceStatusExpired, db.CashSliceStatusReclaimed, db.CashSliceStatusWrittenOff:
		return []string{status}, nil
	case legacyCashStatusClaimed:
		return []string{db.CashSliceStatusRedeemed, db.CashSliceStatusSplit}, nil
	default:
		// Previously an unknown status silently returned an empty page, which
		// is indistinguishable from "a hub with no such slices" — a typo in a
		// client looked like real data.
		return nil, fmt.Errorf("%w: unknown status %q", constants.ErrInvalidParams, status)
	}
}

// legacyCashStatusClaimed is the pre-split status value. Kept accepted (and
// still emitted in the counts) so a client that has not been updated keeps
// working across a deploy.
const legacyCashStatusClaimed = "claimed"

// ListCashWalletClaims returns one page of a hub's slices — live and archived
// merged — plus the total matching that filter and counts over the whole
// unfiltered set.
//
// Filtering, counting and paging all happen in SQL. They used to happen in Go
// over every row of the hub, which was tolerable while rows were bounded by
// live bills; the archive is retained forever, so a year-old hub would have
// loaded its entire history to render twenty rows.
func (svc *appsService) ListCashWalletClaims(f CashClaimFilter) ([]CashClaimRow, uint64, map[string]uint64, error) {
	statuses, err := cashStatusFilterValues(f.Status)
	if err != nil {
		return nil, 0, nil, err
	}

	// Counts come from the unfiltered set on purpose: a UI's facet counts must
	// not change when a facet is selected.
	counts := map[string]uint64{}
	type countRow struct {
		Status string
		N      uint64
	}
	var countRows []countRow
	if err := svc.db.Raw(
		`SELECT u.status AS status, COUNT(*) AS n FROM (`+cashClaimUnionSQL+`) u GROUP BY u.status`,
		f.Now, f.HubID, f.HubID,
	).Scan(&countRows).Error; err != nil {
		return nil, 0, nil, fmt.Errorf("failed to count cash slices for hub %d: %w", f.HubID, err)
	}
	var total uint64
	for _, c := range countRows {
		counts[c.Status] = c.N
		total += c.N
	}
	counts[legacyCashStatusClaimed] = counts[db.CashSliceStatusRedeemed] + counts[db.CashSliceStatusSplit]

	matching := total
	if len(statuses) > 0 {
		matching = 0
		for _, s := range statuses {
			matching += counts[s]
		}
	}

	// Ordered by created_at to preserve the previous newest-first behaviour,
	// with archived and id as tiebreakers: slices minted in one batch share a
	// timestamp to the second, and without a deterministic tiebreaker a row
	// can appear on two pages or on none.
	query := `SELECT * FROM (` + cashClaimUnionSQL + `) u`
	args := []interface{}{f.Now, f.HubID, f.HubID}
	if len(statuses) > 0 {
		query += ` WHERE u.status IN (?)`
		args = append(args, statuses)
	}
	query += ` ORDER BY u.created_at DESC, u.archived ASC, u.id DESC`
	if f.Limit > 0 {
		query += ` LIMIT ? OFFSET ?`
		args = append(args, f.Limit, f.Offset)
	}

	var rows []CashClaimRow
	if err := svc.db.Raw(query, args...).Scan(&rows).Error; err != nil {
		return nil, 0, nil, fmt.Errorf("failed to list cash slices for hub %d: %w", f.HubID, err)
	}
	return rows, matching, counts, nil
}
