package nip47

import (
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/ohstr/nmilat/nipcash"
	"github.com/ohstr/nmilat/nipcash/transport"
	"github.com/ohstr/nmilat/nipcw"
	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/cashrequest"
	"github.com/flokiorg/lokihub/circlelife"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/logger"
	"github.com/flokiorg/lokihub/nip47/cipher"
)

// lookupsPerHour bounds the cash_lookup items one item-proof signer, or one
// envelope for a bearer lookup, may send an hour. Never keyed by a bill.
const lookupsPerHour = 600

// errRebuildMismatch: a rebuilt token differs from the one the bill was issued
// with. Answered INTERNAL, never with a different token.
var errRebuildMismatch = errors.New("rebuilt token differs from the issued token")

// lookupTargetKeys resolves a cash_lookup's target to the connection keys its
// bill proof may recover to (NIP-CASH §Looking Up a Call, I2): a live app (bill,
// Cash Hub or Circle Wallet Hub connection) by its wallet pubkey, a circle Hub
// by every live connection of it; a circle Hub already gone by the connections
// its retained join records name; a record naming it as target; a bill a record
// created, whose pairing key is re-derived; a destroyed bill whose archive is
// still in its retention window. nil when the Hub holds no such target.
func (svc *nip47Service) lookupTargetKeys(target string) ([]string, error) {
	app, err := svc.resolveItemTarget(target)
	if err == nil {
		if app.Kind == db.AppKindCircleHub {
			return svc.liveHubConnKeys(&app), nil
		}
		return []string{app.AppPubkey}, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	var hubConns []string
	if err := svc.db.Model(&db.CircleJoinRecord{}).Where("hub_wallet_pubkey = ?", target).
		Distinct().Pluck("hub_conn_pubkey", &hubConns).Error; err != nil {
		return nil, err
	}
	if len(hubConns) > 0 {
		return hubConns, nil
	}
	var rec db.CashRequestRecord
	err = svc.db.Where("target_wallet_pubkey = ?", target).Order("id DESC").First(&rec).Error
	if err == nil {
		return []string{rec.TargetConnPubkey}, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	var bill db.CashRequestRecordBill
	err = svc.db.Where("wallet_pubkey = ?", target).First(&bill).Error
	if err == nil {
		priv, keyErr := svc.keys.GetCashPairingKey(bill.WalletAppID)
		if keyErr != nil {
			return nil, keyErr
		}
		pub, keyErr := nostr.GetPublicKey(priv)
		if keyErr != nil {
			return nil, keyErr
		}
		return []string{pub}, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	if tomb, ok := db.SpentBillTombstoneFor(svc.db, target); ok && db.RetentionWindowOpen(tomb.RetainedUntil, time.Now()) {
		return []string{tomb.ConnectionPubkey}, nil
	}
	return nil, nil
}

// servePrivateLookup serves a cash_lookup item (NIP-CASH §Looking Up a Call). It
// writes nothing: no request event, no mark, no consumed proof, no record.
func (svc *nip47Service) servePrivateLookup(item transport.Item, binding PrivateItemBinding, billSigner string, billSigned bool) (transport.Result, bool) {
	refuse := func(code, message string) (transport.Result, bool) {
		return transport.Result{ID: item.ID, ResultType: item.Method,
			Error: &transport.ResultError{Code: code, Message: message}}, true
	}

	// I2 and I3: the bill proof verifies against any live connection of the
	// target (VerifyBillProofAny's rule, billProofLive); a bill has exactly one.
	keys, err := svc.lookupTargetKeys(item.Target)
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to resolve a lookup's target")
		omitted(item, "could not resolve the lookup target")
		return transport.Result{}, false
	}
	if len(keys) == 0 {
		omitted(item, "the hub holds no lookup target with this wallet_pubkey")
		return transport.Result{}, false
	}
	if !billSigned || !billProofLive(billSigner, keys) {
		omitted(item, "lookup bill proof missing or not signed by a live connection of the target")
		return transport.Result{}, false
	}

	// I8: never both a proof and a cash_secret.
	if !transport.BearerShapeOK(item.Method, item.HasProof(), item.IsBearer()) {
		return refuse(constants.ERROR_BAD_REQUEST, "an item must carry either a slice proof or a cash secret, never both")
	}

	var params nipcash.CashLookupRequest
	if err := json.Unmarshal(item.Params, &params); err != nil {
		return refuse(constants.ERROR_BAD_REQUEST, "cash_lookup params do not parse")
	}
	if err := params.Validate(); err != nil {
		return refuse(constants.ERROR_BAD_REQUEST, err.Error())
	}

	// I9: a present item proof must verify with its usual bindings.
	signer := ""
	if item.HasProof() {
		paramsHash, err := transport.CanonicalParamsHash(item.Params)
		if err == nil {
			signer, err = transport.VerifyItemProof(item.Proof, transport.ProofBinding{
				Target: item.Target, HubXOnly: binding.HubXOnly, Method: item.Method,
				ParamsHash: paramsHash, Nonce: binding.Nonce, NotAfter: binding.NotAfter,
			}, time.Now())
		}
		if err != nil {
			return refuse(constants.ERROR_UNAUTHORIZED, "this item's proof does not verify")
		}
	}

	limitKey := "lookup:" + signer
	if signer == "" {
		limitKey = "lookup-envelope:" + binding.Nonce
	}
	if svc.lookupLimiter != nil && !svc.lookupLimiter.Allow(limitKey, lookupsPerHour) {
		return refuse(constants.ERROR_RATE_LIMITED, "too many lookups; retry later")
	}

	answer := func(res nipcash.CashLookupResult) (transport.Result, bool) {
		body, err := json.Marshal(res)
		if err != nil {
			return refuse(constants.ERROR_INTERNAL, "could not encode the lookup")
		}
		return transport.Result{ID: item.ID, ResultType: item.Method, Result: body}, true
	}

	if params.ConnectionPubkey != "" {
		return svc.serveJoinLookup(item, params, signer, answer, refuse)
	}

	store := cashrequest.New(svc.db)
	rec, err := store.Get(params.RequestID)
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to read a request record")
		return refuse(constants.ERROR_INTERNAL, "the hub could not read its records, please retry")
	}

	if rec != nil {
		if rec.TargetWalletPubkey != item.Target {
			return refuse(constants.ERROR_UNAUTHORIZED, "this lookup does not match the call's record")
		}
		switch rec.AuthorizerKind {
		case "commitment":
			commitment, cErr := nipcash.CashCommitment(params.CashSecret)
			if item.HasProof() || params.CashSecret == "" || cErr != nil ||
				subtle.ConstantTimeCompare([]byte(commitment), []byte(rec.Authorizer)) != 1 {
				return refuse(constants.ERROR_UNAUTHORIZED, "this lookup does not match the call's record")
			}
		default:
			if signer == "" || signer != rec.Authorizer {
				return refuse(constants.ERROR_UNAUTHORIZED, "this lookup does not match the call's record")
			}
		}
		result, err := svc.rebuildResult(rec, time.Now())
		if err != nil {
			logger.Logger.Error().Err(err).Str("request_id", rec.RequestID).Msg("Failed to rebuild a call's result")
			return refuse(constants.ERROR_INTERNAL, "the hub could not rebuild this call's result")
		}
		return answer(nipcash.CashLookupResult{Status: nipcash.LookupDone, Result: result})
	}

	// No record. A bearer item's secret, when present, must match the commitment
	// the Hub holds for the target's slice; a target holding none matches nothing.
	if params.CashSecret != "" {
		if !svc.lookupSecretMatches(item.Target, params.CashSecret) {
			return refuse(constants.ERROR_UNAUTHORIZED, "this cash secret does not match this target")
		}
	}
	h, err := cashrequest.HorizonTx(svc.db)
	if err != nil {
		return refuse(constants.ERROR_INTERNAL, "the hub could not read its horizon, please retry")
	}
	// The H in force: the configured one, raised at start, never below the stored.
	h = max(h, svc.cfg.PrivateEnvelopeLimits().MaxRequestAgeSeconds)
	decision := nipcash.DecideLookup(nipcash.LookupInput{Now: time.Now().Unix(), Deadline: params.Deadline, Horizon: h})
	return answer(nipcash.CashLookupResult{Status: decision.Status, RetryAfter: decision.RetryAfter})
}

// lookupSecretMatches reports whether secret opens the target bill's cash-mode
// slice.
func (svc *nip47Service) lookupSecretMatches(target, secret string) bool {
	commitment, err := nipcash.CashCommitment(secret)
	if err != nil {
		return false
	}
	var claims []db.CashWalletClaim
	if err := svc.db.Joins("JOIN apps ON apps.id = cash_wallet_claims.wallet_app_id").
		Where("apps.wallet_pubkey = ? AND cash_wallet_claims.identity_type = ?", target, db.CashIdentityCash).
		Find(&claims).Error; err != nil {
		return false
	}
	match := false
	for _, c := range claims {
		if subtle.ConstantTimeCompare([]byte(commitment), []byte(c.IdentityValue)) == 1 {
			match = true
		}
	}
	return match
}

// rebuildResult is a recorded call's original response, rebuilt (NIP-CASH
// §Looking Up a Call, done): the same fields in the same order, every token
// re-encoded from the record and encrypted to the same key (a fresh NIP-44
// nonce), and a destroyed bill's token replaced by its tombstone.
func (svc *nip47Service) rebuildResult(rec *db.CashRequestRecord, now time.Time) (json.RawMessage, error) {
	bills, err := cashrequest.New(svc.db).Bills(rec.ID)
	if err != nil {
		return nil, err
	}
	byRole := map[string]*db.CashRequestRecordBill{}
	for i := range bills {
		byRole[bills[i].Role] = &bills[i]
	}
	token := func(role string) (string, nipcash.TokenOrTombstone, error) {
		b := byRole[role]
		if b == nil {
			return "", nipcash.TokenOrTombstone{}, nil
		}
		t, err := svc.rebuildToken(b, rec.DeliveryPubkey, now)
		return b.WalletPubkey, t, err
	}

	switch rec.Method {
	case constants.NIP47MethodCashOrder:
		// The order as it stands, never a copy of the first reply (NIP-CO
		// §Reading an Order).
		return svc.orderView(rec)
	case constants.NIP47MethodCashWithdraw:
		return svc.withdrawLookupResult(rec, now)
	case constants.NIP47MethodMintCash:
		var meta struct {
			ExpiresAt  *int64                    `json:"expires_at,omitempty"`
			Recipients []nipcash.RecipientResult `json:"recipients"`
		}
		if err := json.Unmarshal([]byte(rec.ResultMeta), &meta); err != nil {
			return nil, err
		}
		role := db.CashRecordBillMinted
		if byRole[role] == nil && len(bills) > 0 {
			role = bills[0].Role
		}
		pub, tok, err := token(role)
		if err != nil {
			return nil, err
		}
		return json.Marshal(struct {
			WalletPubkey string                    `json:"wallet_pubkey"`
			CashToken    nipcash.TokenOrTombstone  `json:"cash_token"`
			ExpiresAt    *int64                    `json:"expires_at,omitempty"`
			Recipients   []nipcash.RecipientResult `json:"recipients"`
		}{pub, tok, meta.ExpiresAt, meta.Recipients})

	case constants.NIP47MethodCashTransfer:
		var meta struct {
			AmountMillis          uint64  `json:"amount_millis"`
			IdentityType          string  `json:"identity_type"`
			IdentityValue         string  `json:"identity_value,omitempty"`
			RemainingAmountMillis *uint64 `json:"remaining_amount_millis,omitempty"`
		}
		if err := json.Unmarshal([]byte(rec.ResultMeta), &meta); err != nil {
			return nil, err
		}
		out := struct {
			AmountMillis          uint64                    `json:"amount_millis"`
			IdentityType          string                    `json:"identity_type"`
			IdentityValue         string                    `json:"identity_value,omitempty"`
			RemainingAmountMillis *uint64                   `json:"remaining_amount_millis,omitempty"`
			NewWalletPubkey       string                    `json:"new_wallet_pubkey,omitempty"`
			NewWalletToken        *nipcash.TokenOrTombstone `json:"new_wallet_token,omitempty"`
			RemainderWalletPubkey string                    `json:"remainder_wallet_pubkey,omitempty"`
			RemainderWalletToken  *nipcash.TokenOrTombstone `json:"remainder_wallet_token,omitempty"`
			IssuedAt              *uint64                   `json:"issued_at,omitempty"`
			ExpiresAt             *uint64                   `json:"expires_at,omitempty"`
		}{AmountMillis: meta.AmountMillis, IdentityType: meta.IdentityType, IdentityValue: meta.IdentityValue,
			RemainingAmountMillis: meta.RemainingAmountMillis}
		carvedRole := db.CashRecordBillCarved
		if byRole[carvedRole] == nil && byRole[db.CashRecordBillSingle] != nil {
			carvedRole = db.CashRecordBillSingle
		}
		if b := byRole[carvedRole]; b != nil {
			pub, tok, err := token(carvedRole)
			if err != nil {
				return nil, err
			}
			out.NewWalletPubkey, out.NewWalletToken = pub, &tok
			if b.ZapIssuedAt != nil && b.ZapExpiresAt != nil {
				issued, expires := uint64(*b.ZapIssuedAt), uint64(*b.ZapExpiresAt) //nolint:gosec // unix times
				out.IssuedAt, out.ExpiresAt = &issued, &expires
			}
		}
		if byRole[db.CashRecordBillRemainder] != nil {
			pub, tok, err := token(db.CashRecordBillRemainder)
			if err != nil {
				return nil, err
			}
			out.RemainderWalletPubkey, out.RemainderWalletToken = pub, &tok
		}
		return json.Marshal(out)

	case constants.NIP47MethodCashConsolidate:
		var meta struct {
			AmountMillis uint64 `json:"amount_millis"`
			ExpiresAt    *int64 `json:"expires_at,omitempty"`
		}
		if err := json.Unmarshal([]byte(rec.ResultMeta), &meta); err != nil {
			return nil, err
		}
		pub, tok, err := token(db.CashRecordBillMerged)
		if err != nil {
			return nil, err
		}
		return json.Marshal(struct {
			AmountMillis    uint64                   `json:"amount_millis"`
			NewWalletPubkey string                   `json:"new_wallet_pubkey"`
			NewWalletToken  nipcash.TokenOrTombstone `json:"new_wallet_token"`
			ExpiresAt       *int64                   `json:"expires_at,omitempty"`
		}{meta.AmountMillis, pub, tok, meta.ExpiresAt})
	}
	return nil, fmt.Errorf("no rebuild for method %q", rec.Method)
}

// rebuildToken is one created bill's token as issued, or its tombstone. A live
// bill's token is re-encoded from the record and must equal the issued one
// byte for byte; it is then encrypted to delivery when the original was.
func (svc *nip47Service) rebuildToken(b *db.CashRequestRecordBill, delivery *string, now time.Time) (nipcash.TokenOrTombstone, error) {
	var app db.App
	err := svc.db.Where("id = ? AND wallet_pubkey = ?", b.WalletAppID, b.WalletPubkey).First(&app).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nipcash.TokenOrTombstone{Tombstone: svc.archivedTombstone(b.WalletPubkey, now)}, nil
	}
	if err != nil {
		return nipcash.TokenOrTombstone{}, err
	}

	secret, err := svc.keys.GetCashPairingKey(b.WalletAppID)
	if err != nil {
		return nipcash.TokenOrTombstone{}, err
	}
	var relays []string
	if err := json.Unmarshal([]byte(b.RelayHints), &relays); err != nil {
		return nipcash.TokenOrTombstone{}, err
	}
	sig, err := hex.DecodeString(b.Signature)
	if err != nil {
		return nipcash.TokenOrTombstone{}, err
	}
	amount := uint64(b.AmountMillis) //nolint:gosec // non-negative
	tok := nipcash.Token{
		HRP:                  constants.CashTokenHRP,
		WalletPubkey:         b.WalletPubkey,
		Secret:               secret,
		RelayURLs:            relays,
		MintSignature:        sig,
		AttestedAmountMillis: &amount,
		Scheme:               nipcash.SignatureScheme(b.Scheme),
	}
	if b.IdentityRequired != nil {
		v := *b.IdentityRequired == 1
		tok.IdentityRequired = &v
	}
	if b.HubGroup != nil {
		if tok.HubGroup, err = hex.DecodeString(*b.HubGroup); err != nil {
			return nipcash.TokenOrTombstone{}, err
		}
	}
	encoded, err := nipcash.Encode(tok)
	if err != nil {
		return nipcash.TokenOrTombstone{}, err
	}
	if app.CashToken != "" && app.CashToken != encoded {
		logger.Logger.Error().Uint("wallet_app_id", b.WalletAppID).Msg("A rebuilt cash token differs from the issued one")
		return nipcash.TokenOrTombstone{}, errRebuildMismatch
	}
	if delivery == nil {
		return nipcash.TokenOrTombstone{Token: encoded}, nil
	}
	walletKey, err := svc.keys.GetAppWalletKey(b.WalletAppID)
	if err != nil {
		return nipcash.TokenOrTombstone{}, err
	}
	c, err := cipher.NewNip47Cipher(constants.ENCRYPTION_TYPE_NIP44_V2, *delivery, walletKey)
	if err != nil {
		return nipcash.TokenOrTombstone{}, err
	}
	enc, err := c.Encrypt(encoded)
	if err != nil {
		return nipcash.TokenOrTombstone{}, err
	}
	return nipcash.TokenOrTombstone{Token: enc}, nil
}

// archivedTombstone is a destroyed bill's tombstone in a rebuilt result: with
// its reason, and retained_until while its window is open (status and reason
// alone after it).
func (svc *nip47Service) archivedTombstone(walletPubkey string, now time.Time) *nipcash.Tombstone {
	t := &nipcash.Tombstone{Status: nipcash.StatusSpent}
	if reason, ok := db.SpentBillReason(svc.db, walletPubkey); ok {
		t.Reason = reason
	}
	if tomb, ok := db.SpentBillTombstoneFor(svc.db, walletPubkey); ok && db.RetentionWindowOpen(tomb.RetainedUntil, now) {
		at := tomb.RetainedUntil.Unix()
		t.RetainedUntil = &at
	}
	return t
}

// serveJoinLookup answers a NIP-CW join lookup (§Lost Join Reply): authorized by
// the joiner's item proof, matched on (signer, connection_pubkey) at this Hub,
// answered with the join's own encrypted_details, or by its deadline with the
// 6-minute window. It writes nothing.
func (svc *nip47Service) serveJoinLookup(item transport.Item, params nipcash.CashLookupRequest, signer string,
	answer func(nipcash.CashLookupResult) (transport.Result, bool), refuse func(code, message string) (transport.Result, bool)) (transport.Result, bool) {
	if signer == "" {
		return refuse(constants.ERROR_UNAUTHORIZED, "a join lookup needs the joiner's item proof")
	}
	var rec db.CircleJoinRecord
	err := svc.db.Where("hub_wallet_pubkey = ? AND authorizer_pubkey = ? AND connection_pubkey = ?",
		item.Target, signer, params.ConnectionPubkey).First(&rec).Error
	if err == nil {
		body, err := json.Marshal(map[string]string{"encrypted_details": rec.EncryptedDetails})
		if err != nil {
			return refuse(constants.ERROR_INTERNAL, "could not encode the join")
		}
		return answer(nipcash.CashLookupResult{Status: nipcash.LookupDone, Result: body})
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		logger.Logger.Error().Err(err).Msg("Failed to read a join record")
		return refuse(constants.ERROR_INTERNAL, "the hub could not read its records, please retry")
	}
	// No join to this key: the rotation that installed it, or will, answers
	// (NIP-CW §Recovering a Lost Connection), read from its row and the clock
	// alone; the lookup writes nothing.
	now := time.Now()
	outcome, rotation, err := circlelife.LookupRotation(svc.db, signer, params.ConnectionPubkey, now)
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to read a rotation")
		return refuse(constants.ERROR_INTERNAL, "the hub could not read its records, please retry")
	}
	switch outcome {
	case circlelife.RotationPendingUntil:
		return answer(nipcash.CashLookupResult{Status: nipcash.LookupPending, RetryAfter: rotation.CompletesAt})
	case circlelife.RotationDone:
		body, err := svc.rotatedJoinDetails(item.Target, signer, rotation)
		if err != nil {
			logger.Logger.Error().Err(err).Uint("wallet_app_id", rotation.WalletAppID).Msg("Failed to rebuild a rotated wallet's details")
			return refuse(constants.ERROR_INTERNAL, "the hub could not rebuild this wallet's details")
		}
		return answer(nipcash.CashLookupResult{Status: nipcash.LookupDone, Result: body})
	}
	decision := nipcash.DecideLookup(nipcash.LookupInput{Now: now.Unix(), Deadline: params.Deadline, Join: true})
	return answer(nipcash.CashLookupResult{Status: decision.Status, RetryAfter: decision.RetryAfter})
}

// rotatedJoinDetails is the join's encrypted_details of the wallet rotation
// moved, with connection_pubkey set to the new key, encrypted to the member
// exactly as at join (from the Hub's connection key).
func (svc *nip47Service) rotatedJoinDetails(hubWalletPubkey, member string, rotation *db.CircleRotation) (json.RawMessage, error) {
	var rec db.CircleJoinRecord
	if err := svc.db.Where("wallet_app_id = ? AND authorizer_pubkey = ? AND hub_wallet_pubkey = ?", rotation.WalletAppID, member, hubWalletPubkey).
		Order("id DESC").First(&rec).Error; err != nil {
		return nil, err
	}
	hubPriv, err := svc.keys.GetAppWalletKey(rec.CircleHubAppID)
	if err != nil {
		return nil, err
	}
	c, err := cipher.NewNip47Cipher(constants.ENCRYPTION_TYPE_NIP44_V2, member, hubPriv)
	if err != nil {
		return nil, err
	}
	plain, err := c.Decrypt(rec.EncryptedDetails)
	if err != nil {
		return nil, err
	}
	var details nipcw.CircleWalletDetails
	if err := json.Unmarshal([]byte(plain), &details); err != nil {
		return nil, err
	}
	details.ConnectionPubkey = rotation.NewConnectionPubkey
	wrapped, err := nipcw.EncryptDetails(hubPriv, member, details)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(wrapped), nil
}
