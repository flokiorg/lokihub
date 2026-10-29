package circlelife

import (
	"errors"
	"fmt"
	"time"

	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nipcw"
	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/db"
)

// The notifications of NIP-CW §Recovering a Lost Connection.
const (
	NoticeRotationPending   = "rotation_pending"
	NoticeRotationCancelled = "rotation_cancelled"
	NoticeRotationCompleted = "rotation_completed"
	NoticeWalletFrozen      = "wallet_frozen"
)

// Notice is a notification to send, after commit, to a Circle Wallet's current
// connection key.
type Notice struct {
	WalletAppID  uint
	Type         string
	Notification map[string]interface{}
}

// MemberRequestResult is a member request's outcome: Code is "" when it was
// applied, else the NIP-CW code it was refused with.
type MemberRequestResult struct {
	Code        string `json:"code,omitempty"`
	Message     string `json:"message"`
	Action      string `json:"action,omitempty"`
	WalletAppID uint   `json:"wallet_app_id,omitempty"`
	CompletesAt *int64 `json:"completes_at,omitempty"`
	// Notices go out after commit.
	Notices []Notice `json:"-"`
}

func refused(code, message string) MemberRequestResult {
	return MemberRequestResult{Code: code, Message: message}
}

// ApplyMemberRequest checks and applies a member request the host submitted for
// the Hub hub (NIP-CW §Recovering a Lost Connection), in the spec's order,
// refusing at the first step it fails: rotation served (NOT_IMPLEMENTED); the
// request itself and its single use, by nmilat's VerifyMemberRequest
// (UNAUTHORIZED, REQUEST_REUSED; its id is recorded at step 3 and stays
// consumed whatever follows); the wallet (NOT_FOUND); the member, the join's
// pubkey (UNAUTHORIZED); the wallet's delay (NOT_IMPLEMENTED); and the action.
// A rotate is checked and accepted under the node-wide connection-key lock,
// with any join's step 9: ROTATION_PENDING while one is pending, RESTRICTED for
// a key not valid, in use on the node or named by a pending rotation. A freeze
// of a frozen wallet, or an unfreeze of one that is not, changes nothing.
func ApplyMemberRequest(gormDB *gorm.DB, hub *db.App, cfg *db.CircleHubConfig, ev *nip01.Event, now time.Time) (MemberRequestResult, error) {
	// A Hub that stopped offering rotation still serves the wallets it issued
	// with a delay, fixed for their life; step 6 refuses the others.
	served := cfg.RotationEnabled
	if !served {
		var n int64
		if err := gormDB.Model(&db.App{}).Where("parent_app_id = ? AND kind = ? AND circle_rotation_delay_secs IS NOT NULL",
			hub.ID, db.AppKindCircleWallet).Count(&n).Error; err != nil {
			return MemberRequestResult{}, err
		}
		served = n > 0
	}
	if !served {
		return refused(nipcw.ErrNotImplemented, "this Hub does not serve member requests"), nil
	}
	if hub.WalletPubkey == nil {
		return MemberRequestResult{}, fmt.Errorf("circle hub %d has no wallet pubkey", hub.ID)
	}
	var recordErr error
	consumed := func(id string) bool {
		err := gormDB.Create(&db.CircleWalletIdentityProof{AppID: hub.ID, EventID: id, Kind: db.CircleProofMember,
			RetainUntil: nipcw.MemberRequestRetainUntil(int64(ev.CreatedAt))}).Error //nolint:gosec // unix seconds
		if err == nil {
			return false
		}
		if !db.IsDuplicateKeyError(err) {
			recordErr = err
		}
		return true
	}
	mr, err := nipcw.VerifyMemberRequest(ev, *hub.WalletPubkey, now, consumed)
	if recordErr != nil {
		return MemberRequestResult{}, recordErr
	}
	if err != nil {
		var joinErr *nipcw.JoinProofError
		if errors.As(err, &joinErr) {
			return refused(joinErr.Code, joinErr.Reason), nil
		}
		return refused(nipcw.ErrUnauthorized, err.Error()), nil
	}

	var wallet db.App
	if err := gormDB.Where("wallet_pubkey = ? AND kind = ? AND parent_app_id = ?", mr.Wallet, db.AppKindCircleWallet, hub.ID).
		Limit(1).Find(&wallet).Error; err != nil {
		return MemberRequestResult{}, err
	}
	if wallet.ID == 0 {
		return refused(nipcw.ErrNotFound, "this Hub holds no such wallet"), nil
	}
	member, err := joinPubkey(gormDB, &wallet)
	if err != nil {
		return MemberRequestResult{}, err
	}
	if member == "" || member != mr.Pubkey {
		return refused(nipcw.ErrUnauthorized, "a member request must be signed by the pubkey of the join that created the wallet"), nil
	}
	if wallet.CircleRotationDelaySecs == nil {
		return refused(nipcw.ErrNotImplemented, "this wallet was issued without a rotation delay"), nil
	}
	result := MemberRequestResult{Action: string(mr.Action), WalletAppID: wallet.ID, Message: "applied"}

	switch mr.Action {
	case nipcw.ActionFreeze, nipcw.ActionUnfreeze:
		freeze := mr.Action == nipcw.ActionFreeze
		if wallet.CircleFrozen == freeze {
			result.Message = "no change"
			return result, nil
		}
		if err := gormDB.Model(&db.App{}).Where("id = ?", wallet.ID).Update("circle_frozen", freeze).Error; err != nil {
			return MemberRequestResult{}, err
		}
		if freeze {
			result.Notices = append(result.Notices, Notice{WalletAppID: wallet.ID, Type: NoticeWalletFrozen, Notification: map[string]interface{}{}})
		}
		return result, nil
	default:
		// ActionRotate is handled below.
	}

	if err := mr.CheckConnection(); err != nil {
		var joinErr *nipcw.JoinProofError
		if errors.As(err, &joinErr) {
			return refused(joinErr.Code, joinErr.Reason), nil
		}
		return refused(nipcw.ErrRestricted, err.Error()), nil
	}
	var outcome MemberRequestResult
	err = gormDB.Transaction(func(tx *gorm.DB) error {
		if err := db.LockNamespace(tx, db.LockNsConnKey, 0); err != nil {
			return err
		}
		var pending int64
		if err := tx.Model(&db.CircleRotation{}).Where("wallet_app_id = ? AND state = ?", wallet.ID, db.CircleRotationPending).
			Count(&pending).Error; err != nil {
			return err
		}
		if pending > 0 {
			outcome = refused(nipcw.ErrRotationPending, "this wallet already has a pending rotation")
			return nil
		}
		taken, err := ConnectionKeyTaken(tx, mr.Connection)
		if err != nil {
			return err
		}
		if taken {
			outcome = refused(nipcw.ErrRestricted, "the new connection key is in use or pending on this node")
			return nil
		}
		completes := now.Unix() + *wallet.CircleRotationDelaySecs
		rotation := db.CircleRotation{WalletAppID: wallet.ID, MemberPubkey: mr.Pubkey, NewConnectionPubkey: mr.Connection,
			RequestedAt: now.Unix(), CompletesAt: completes, State: db.CircleRotationPending}
		if err := tx.Create(&rotation).Error; err != nil {
			if db.IsDuplicateKeyError(err) {
				outcome = refused(nipcw.ErrRestricted, "the new connection key is pending in another rotation")
				return nil
			}
			return err
		}
		outcome = result
		outcome.CompletesAt = &completes
		outcome.Notices = []Notice{{WalletAppID: wallet.ID, Type: NoticeRotationPending,
			Notification: map[string]interface{}{"connection_pubkey": mr.Connection, "completes_at": completes}}}
		return nil
	})
	return outcome, err
}

// joinPubkey is the pubkey of the join that created wallet.
func joinPubkey(tx *gorm.DB, wallet *db.App) (string, error) {
	var record db.CircleJoinRecord
	if err := tx.Where("wallet_app_id = ?", wallet.ID).Order("id DESC").Limit(1).Find(&record).Error; err != nil {
		return "", err
	}
	if record.ID != 0 {
		return record.AuthorizerPubkey, nil
	}
	var membership db.CircleWalletMembership
	if err := tx.Where("wallet_app_id = ?", wallet.ID).Limit(1).Find(&membership).Error; err != nil {
		return "", err
	}
	return membership.RequesterPubkey, nil
}

// ConnectionKeyTaken reports whether key is any app's connection key on this
// node or the new key of a pending rotation. Callers hold the connection-key
// lock.
func ConnectionKeyTaken(tx *gorm.DB, key string) (bool, error) {
	var apps, rotations int64
	if err := tx.Model(&db.App{}).Where("app_pubkey = ?", key).Count(&apps).Error; err != nil {
		return false, err
	}
	if err := tx.Model(&db.CircleRotation{}).Where("new_connection_pubkey = ? AND state = ?", key, db.CircleRotationPending).
		Count(&rotations).Error; err != nil {
		return false, err
	}
	return apps > 0 || rotations > 0, nil
}

// CancelPendingRotation cancels a wallet's pending rotation that has not
// reached its completion: a request validly signed by the wallet's current key,
// while the wallet is not frozen, cancels it (NIP-CW §Recovering a Lost
// Connection). The notice goes to that same key.
func CancelPendingRotation(gormDB *gorm.DB, walletID uint, now time.Time) ([]Notice, error) {
	var notices []Notice
	err := gormDB.Transaction(func(tx *gorm.DB) error {
		var rotation db.CircleRotation
		if err := tx.Where("wallet_app_id = ? AND state = ? AND completes_at > ?", walletID, db.CircleRotationPending, now.Unix()).
			Limit(1).Find(&rotation).Error; err != nil || rotation.ID == 0 {
			return err
		}
		ended := now.Unix()
		res := tx.Model(&db.CircleRotation{}).Where("id = ? AND state = ?", rotation.ID, db.CircleRotationPending).
			Updates(map[string]interface{}{"state": db.CircleRotationCancelled, "ended_at": ended})
		if res.Error != nil || res.RowsAffected == 0 {
			return res.Error
		}
		notices = append(notices, Notice{WalletAppID: walletID, Type: NoticeRotationCancelled,
			Notification: map[string]interface{}{"connection_pubkey": rotation.NewConnectionPubkey}})
		return nil
	})
	return notices, err
}

// CompleteDueRotations completes every pending rotation whose completes_at has
// come (now at or after it), matching filter, under the connection-key lock: if
// the new key is in use on the node by then, the rotation is cancelled and the
// old key told; otherwise the new key replaces the old one, the wallet is
// unfrozen, and the new key is told. It returns the notices and the wallets
// whose key changed.
func CompleteDueRotations(gormDB *gorm.DB, now time.Time, filter func(*gorm.DB) *gorm.DB) ([]Notice, []uint, error) {
	var notices []Notice
	var swapped []uint
	err := gormDB.Transaction(func(tx *gorm.DB) error {
		if err := db.LockNamespace(tx, db.LockNsConnKey, 0); err != nil {
			return err
		}
		q := tx.Where("state = ? AND completes_at <= ?", db.CircleRotationPending, now.Unix())
		if filter != nil {
			q = filter(q)
		}
		var due []db.CircleRotation
		if err := q.Find(&due).Error; err != nil {
			return err
		}
		for _, rotation := range due {
			ended := now.Unix()
			var inUse int64
			if err := tx.Model(&db.App{}).Where("app_pubkey = ? AND id <> ?", rotation.NewConnectionPubkey, rotation.WalletAppID).
				Count(&inUse).Error; err != nil {
				return err
			}
			if inUse > 0 {
				if err := tx.Model(&db.CircleRotation{}).Where("id = ?", rotation.ID).
					Updates(map[string]interface{}{"state": db.CircleRotationCancelled, "ended_at": ended}).Error; err != nil {
					return err
				}
				notices = append(notices, Notice{WalletAppID: rotation.WalletAppID, Type: NoticeRotationCancelled,
					Notification: map[string]interface{}{"connection_pubkey": rotation.NewConnectionPubkey}})
				continue
			}
			res := tx.Model(&db.App{}).Where("id = ? AND kind = ?", rotation.WalletAppID, db.AppKindCircleWallet).
				Updates(map[string]interface{}{"app_pubkey": rotation.NewConnectionPubkey, "circle_frozen": false})
			if res.Error != nil {
				return res.Error
			}
			state := db.CircleRotationCompleted
			if res.RowsAffected == 0 {
				state = db.CircleRotationCancelled // the wallet is gone
			}
			if err := tx.Model(&db.CircleRotation{}).Where("id = ?", rotation.ID).
				Updates(map[string]interface{}{"state": state, "ended_at": ended}).Error; err != nil {
				return err
			}
			if state == db.CircleRotationCompleted {
				swapped = append(swapped, rotation.WalletAppID)
				notices = append(notices, Notice{WalletAppID: rotation.WalletAppID, Type: NoticeRotationCompleted,
					Notification: map[string]interface{}{"connection_pubkey": rotation.NewConnectionPubkey}})
			}
		}
		return nil
	})
	return notices, swapped, err
}

// RotationLookup is how a join lookup by (member, connection_pubkey) reads a
// rotation to that key, from its row and the clock alone, writing nothing
// (NIP-CW §Recovering a Lost Connection, review 1): Pending until completes_at;
// Completed once completed, or once due with the key used by no other app;
// otherwise no answer (the lookup is answered by its deadline).
type RotationLookup int

// The answers a rotation gives a join lookup.
const (
	RotationNone RotationLookup = iota
	RotationPendingUntil
	RotationDone
)

// LookupRotation finds the rotation that installed or will install key on a
// wallet of member, and what it answers at now.
func LookupRotation(tx *gorm.DB, member, key string, now time.Time) (RotationLookup, *db.CircleRotation, error) {
	var rotation db.CircleRotation
	if err := tx.Where("member_pubkey = ? AND new_connection_pubkey = ? AND state IN ?", member, key,
		[]string{db.CircleRotationPending, db.CircleRotationCompleted}).Order("id DESC").Limit(1).Find(&rotation).Error; err != nil {
		return RotationNone, nil, err
	}
	switch {
	case rotation.ID == 0:
		return RotationNone, nil, nil
	case rotation.State == db.CircleRotationCompleted:
		return RotationDone, &rotation, nil
	case now.Unix() < rotation.CompletesAt:
		return RotationPendingUntil, &rotation, nil
	}
	var inUse int64
	if err := tx.Model(&db.App{}).Where("app_pubkey = ? AND id <> ?", key, rotation.WalletAppID).Count(&inUse).Error; err != nil {
		return RotationNone, nil, err
	}
	if inUse > 0 {
		return RotationNone, nil, nil
	}
	return RotationDone, &rotation, nil
}
