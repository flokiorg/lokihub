package controllers

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/nbd-wtf/go-nostr"
	"github.com/ohstr/nmilat/nipcash"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/logger"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/flokiorg/lokihub/transactions"
)

// cash_status' response is github.com/ohstr/nmilat/nipcash's own exported
// CashStatusResult/RecipientStatus — same wire shape, adopted directly
// instead of maintaining a parallel copy (nmilat migration). Two
// accepted, tested differences from this controller's former local types:
//   - RecipientStatus's numeric fields (AmountMillis, RedeemFeeMillis,
//     NetRedeemableMillis, MinTransferMillis) are uint64, not int64 - every
//     value here is a Mloki amount or fee derived from one, always
//     non-negative by construction (same invariant the former int64 fields'
//     own //nolint:gosec comments already documented).
//   - RecipientStatus.IdentityValue carries `omitempty`, the former local
//     type's didn't, so an empty identity_value is omitted rather than sent
//     as "". A cash-mode row is not empty: its identity_value is the slice's
//     secret commitment, and it is sent. Only that slice's own holder can
//     read it, because a cash slice is always alone on its bill
//     (cashwallet.Resolve) and a proofless read needs the matching secret.
//
// CashStatusCaller is which recipient is asking. Always known, and never nil in
// practice: cash_status is served over the private transport only, where every
// item carries a proof signed by one specific recipient's own key.
//
// The standard transport could never have populated this, which is why it no
// longer serves the method. That is a fact about the protocol rather than a
// missing feature: every recipient of a bill holds the SAME connection string
// (NIP-CASH §The Pairing Connection), so a Hub receiving cash_status there
// cannot tell one recipient from another and can only answer everyone or no
// one.
//
// IdentityValue is taken from that proof's signer and never from anything the
// item asserts about itself — NIP-CASH §Scoping the Roster requires that
// direction explicitly, since an item that could name its own scope subject
// could read any co-recipient's row by naming theirs.
type CashStatusCaller struct {
	// IdentityValue is the verified signer's identity value, for a
	// pubkey-bound bill.
	IdentityValue string
	// IsCash marks a cash-mode bill, whose slices have no identity of their
	// own — authorization there is the secret in params, and the bill's single
	// cash row is by definition the caller's.
	IsCash bool
}

// HandleCashStatusEvent returns a cash_wallet's recipients — identity, entitled
// amount, and claimed status only. It never includes invoice/preimage/payment
// detail, since a cash_wallet carries no list_transactions grant at all.
//
// How much of the roster comes back depends on `scope`
// (NIP-CASH §Scoping the Roster):
//
//	scope=all    every recipient's row — the shared, transparent view that
//	             matches the model already accepted for get_balance
//	scope=mine   only the calling recipient's own row
//
// Absent means "mine": a caller who says nothing receives the smallest answer
// and learns nothing about their co-recipients. There is no longer a
// transport-dependent default, because there is no longer more than one
// transport for this method.
//
// Note that "mine" is a view, not an authorization boundary. It changes what is
// returned and never what a caller may do, so having asked for it must not
// narrow any later call.
func (controller *nip47Controller) HandleCashStatusEvent(ctx context.Context, nip47Request *models.Request, requestEventId uint, app *db.App, publishResponse publishFunc, caller *CashStatusCaller) {
	if app.Kind != db.AppKindCashWallet {
		respondError(publishResponse, nip47Request.Method, constants.ERROR_RESTRICTED, "cash_status requires a cash_wallet app")
		return
	}

	scope, err := resolveCashStatusScope(nip47Request, caller)
	if err != nil {
		respondError(publishResponse, nip47Request.Method, constants.ERROR_BAD_REQUEST, err.Error())
		return
	}

	claims, err := controller.appsService.ListClaimsForWallet(app.ID)
	if err != nil {
		logger.Logger.Error().Err(err).Uint("app_id", app.ID).Msg("Failed to list Cash wallet recipients")
		respondError(publishResponse, nip47Request.Method, constants.ERROR_INTERNAL, "failed to list recipients")
		return
	}
	if scope == nipcash.ScopeMine {
		claims = claimsForCaller(oneRowPerIdentity(claims), caller)
		// "mine" comes from the item-proof signer alone (NIP-CASH §Scoping the Roster,
		// XQ-5): a caller it names no row for gets NOT_FOUND, never a wider roster. A
		// connection_key holder reads scope "all" and picks its row.
		if len(claims) == 0 {
			respondError(publishResponse, nip47Request.Method, constants.ERROR_NOT_FOUND, "no slice of this bill is registered for the identity that signed this item")
			return
		}
	} else {
		claims = everySliceLiveFirst(claims)
	}

	var expiresAt nipcash.Expiry
	if app.ExpiresAt != nil {
		expiresAt = nipcash.Expiry{At: app.ExpiresAt.Unix(), Present: true}
	}

	recipients := make([]nipcash.RecipientStatus, len(claims))
	for i, c := range claims {
		redeemFeeMloki := transactions.CalculateRedeemFeeMloki(uint64(c.AmountMloki), c.RedeemFeeBaseMloki, c.RedeemFeePpm) //nolint:gosec // AmountMloki is always non-negative
		status := nipcash.RecipientStatus{
			IdentityType:        c.IdentityType,
			IdentityValue:       c.IdentityValue,
			IAPubkey:            iaPubkeyOf(c),
			AmountMillis:        uint64(c.AmountMloki), //nolint:gosec // AmountMloki is always non-negative
			Claimed:             c.ClaimedAt != nil,
			RedeemFeeMillis:     redeemFeeMloki,
			NetRedeemableMillis: uint64(c.AmountMloki) - redeemFeeMloki, //nolint:gosec // redeemFeeMloki <= AmountMloki by construction
			MinTransferMillis:   uint64(c.MinTransferMloki),             //nolint:gosec // MinTransferMloki is always non-negative
			ExpiresAt:           expiresAt,
		}
		if c.ClaimedAt != nil {
			claimedAt := c.ClaimedAt.Unix()
			status.ClaimedAt = &claimedAt
		}
		recipients[i] = status
	}

	result := nipcash.CashStatusResult{Recipients: recipients}
	controller.addWithdrawStatus(app, claims, &result, caller)
	publishResponse(&models.Response{
		ResultType: nip47Request.Method,
		Result:     result,
	}, nostr.Tags{})
}

// resolveCashStatusScope decides how much roster to return, applying the
// transport-specific default and refusing what a transport cannot honour.
func resolveCashStatusScope(nip47Request *models.Request, caller *CashStatusCaller) (string, error) {
	var params nipcash.CashStatusParams
	if len(nip47Request.Params) > 0 {
		if err := json.Unmarshal(nip47Request.Params, &params); err != nil {
			return "", fmt.Errorf("could not parse cash_status params")
		}
	}
	if !nipcash.IsValidCashStatusScope(params.Scope) {
		// Same rule the SDK checks client-side, shared rather than duplicated.
		return "", fmt.Errorf("scope must be %q or %q", nipcash.ScopeAll, nipcash.ScopeMine)
	}

	if caller == nil {
		// Unreachable: cash_status is served over the private transport only, and
		// that path always knows who is asking (the item proof's signer). Kept as a
		// refusal rather than a nil-deref, and rather than quietly widening to the
		// full roster — which is precisely the disclosure scoping exists to prevent.
		return "", fmt.Errorf("cash_status reached the controller with no caller identity; it is served over the private transport only")
	}
	if params.Scope == "" {
		// The default is the smallest answer: a caller who says nothing learns
		// nothing about their co-recipients.
		return nipcash.ScopeMine, nil
	}
	return params.Scope, nil
}

// everySliceLiveFirst lists every slice (NIP-CASH cash_status: "every slice this
// wallet was ever created or split into"), each identity's rows together with
// the one oneRowPerIdentity picks first, so a client taking the first row that
// matches it reads the slice it holds now.
func everySliceLiveFirst(claims []db.CashWalletClaim) []db.CashWalletClaim {
	type key struct{ typ, value, ia string }
	keyOf := func(c db.CashWalletClaim) key { return key{c.IdentityType, c.IdentityValue, c.IAPubkey} }
	rest := map[key][]db.CashWalletClaim{}
	preferred := oneRowPerIdentity(claims)
	picked := make(map[uint]bool, len(preferred))
	for _, c := range preferred {
		picked[c.ID] = true
	}
	for _, c := range claims {
		if !picked[c.ID] {
			rest[keyOf(c)] = append(rest[keyOf(c)], c)
		}
	}
	out := make([]db.CashWalletClaim, 0, len(claims))
	for _, c := range preferred {
		out = append(out, c)
		out = append(out, rest[keyOf(c)]...)
	}
	return out
}

// oneRowPerIdentity keeps one row per identity: its live (unclaimed) row when
// it has one, else its newest claimed row. An identity can hold a claimed
// history row and a live row on one bill, after a slice was transferred in
// place to a former holder; "mine" shows the slice it holds now, exactly one row
// (NIP-CASH §Scoping the Roster). Order is kept.
func oneRowPerIdentity(claims []db.CashWalletClaim) []db.CashWalletClaim {
	type key struct{ typ, value, ia string }
	at := map[key]int{}
	out := make([]db.CashWalletClaim, 0, len(claims))
	for _, c := range claims {
		k := key{c.IdentityType, c.IdentityValue, c.IAPubkey}
		i, seen := at[k]
		if !seen {
			at[k] = len(out)
			out = append(out, c)
			continue
		}
		cur := out[i]
		switch {
		case cur.ClaimedAt == nil:
			// The live row stays.
		case c.ClaimedAt == nil:
			out[i] = c
		case c.ClaimedAt.After(*cur.ClaimedAt) || (c.ClaimedAt.Equal(*cur.ClaimedAt) && c.ID > cur.ID):
			out[i] = c
		}
	}
	return out
}

// claimsForCaller narrows a roster to the asking recipient's own row.
//
// Returns nothing when no row matches. That is deliberate rather than a fallback
// to the full roster: a caller whose proof verified but who holds no slice of
// this bill is entitled to no rows, and widening the answer on no match would
// turn every mismatch into a full roster disclosure.
func claimsForCaller(claims []db.CashWalletClaim, caller *CashStatusCaller) []db.CashWalletClaim {
	if caller == nil {
		return claims
	}
	out := make([]db.CashWalletClaim, 0, 1)
	for _, c := range claims {
		if caller.IsCash {
			if c.IdentityType == db.CashIdentityCash {
				out = append(out, c)
			}
			continue
		}
		if c.IdentityType != db.CashIdentityCash && c.IdentityValue == caller.IdentityValue {
			out = append(out, c)
		}
	}
	return out
}

// iaPubkeyOf is a row's ia_pubkey: a connection_key slice's IA, so its holder can
// pick its own row from scope "all" (NIP-CASH §Cash Status, CT-STAT-15); none for a
// pubkey or cash row.
func iaPubkeyOf(c db.CashWalletClaim) string {
	if c.IdentityType == db.CashIdentityConnectionKey {
		return c.IAPubkey
	}
	return ""
}
