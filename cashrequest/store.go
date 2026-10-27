// Package cashrequest keeps the request_id marks and records of NIP-CASH calls
// (§request_id, §The Record, §Lost Replies), the single horizon H (§Deadlines) and
// the consumed claim proofs (§Claim Proofs).
package cashrequest

import (
	"errors"
	"fmt"
	"time"

	"github.com/ohstr/nmilat/nipcash"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/flokiorg/lokihub/db"
)

var (
	// ErrRequestReused: the request_id is already used on this node (REQUEST_REUSED).
	ErrRequestReused = errors.New("request_id already used on this node")
	// ErrDeadlinePassed: the call may not commit after its deadline (BAD_REQUEST, final).
	ErrDeadlinePassed = errors.New("the request's deadline has passed")
	// ErrProofConsumed: the claim proof was already used.
	ErrProofConsumed = errors.New("this claim proof was already used")
)

// Store reads and writes marks, records and proofs. Now is the clock (tests set it).
type Store struct {
	DB  *gorm.DB
	Now func() time.Time
}

// New is a Store on gormDB with the real clock.
func New(gormDB *gorm.DB) Store { return Store{DB: gormDB, Now: time.Now} }

func (s Store) now() time.Time {
	if s.Now == nil {
		return time.Now()
	}
	return s.Now()
}

// IsUsed reports whether id is marked used (a read-only pre-check; MarkUsed decides).
func (s Store) IsUsed(id string) (bool, error) {
	var n int64
	err := s.DB.Model(&db.CashUsedRequestID{}).Where("request_id = ?", id).Count(&n).Error
	return n > 0, err
}

// MarkUsed marks id used, in its own committed transaction, at the point the call
// is authorized. A later refusal of the call leaves it used (NIP-CASH §request_id).
// The primary key is the atomic test-and-mark: a second mark is ErrRequestReused.
//
// Call it before the call's commit transaction opens, never inside one: sqlite
// has one writer, so a mark taken while the caller holds a write transaction waits
// for that transaction and fails busy.
func (s Store) MarkUsed(id, method string) error {
	if !nipcash.ValidRequestID(id) {
		return fmt.Errorf("request_id %q is malformed", id)
	}
	err := s.DB.Create(&db.CashUsedRequestID{RequestID: id, Method: method, UsedAt: s.now().Unix()}).Error
	if db.IsDuplicateKeyError(err) {
		return ErrRequestReused
	}
	return err
}

// CommitTx writes a call's record and bills inside the call's own commit
// transaction tx. The call may commit only up to its deadline (ErrDeadlinePassed,
// which must roll everything back). A record with no bills (an in-place transfer)
// may be dropped once deadline + H has passed.
func (s Store) CommitTx(tx *gorm.DB, rec *db.CashRequestRecord, bills []db.CashRequestRecordBill) error {
	now := s.now().Unix()
	if !nipcash.CommitAllowed(rec.Deadline, now) {
		return ErrDeadlinePassed
	}
	rec.CommittedAt = now
	if len(bills) == 0 {
		h, err := HorizonTx(tx)
		if err != nil {
			return err
		}
		dropAfter := rec.Deadline + h + 1
		rec.DropAfter = &dropAfter
	}
	if err := tx.Create(rec).Error; err != nil {
		return fmt.Errorf("record request %s: %w", rec.RequestID, err)
	}
	for i := range bills {
		bills[i].RecordID = rec.ID
		bills[i].Ordinal = int16(i) //nolint:gosec // a call creates at most a handful of bills
	}
	if len(bills) > 0 {
		if err := tx.Create(&bills).Error; err != nil {
			return fmt.Errorf("record bills of request %s: %w", rec.RequestID, err)
		}
	}
	return nil
}

// Get is the record of id, or nil when there is none.
func (s Store) Get(id string) (*db.CashRequestRecord, error) {
	var rec db.CashRequestRecord
	err := s.DB.Where("request_id = ?", id).First(&rec).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &rec, nil
}

// Bills are the bills a record created, in order.
func (s Store) Bills(recordID uint) ([]db.CashRequestRecordBill, error) {
	var bills []db.CashRequestRecordBill
	err := s.DB.Where("record_id = ?", recordID).Order("ordinal").Find(&bills).Error
	return bills, err
}

// ConsumeProofTx marks a claim proof used inside tx; a proof already used is
// ErrProofConsumed.
func (s Store) ConsumeProofTx(tx *gorm.DB, eventID string, proofCreatedAt int64, method string) error {
	err := tx.Create(&db.CashConsumedProof{EventID: eventID, ProofCreatedAt: proofCreatedAt, ConsumedAt: s.now().Unix(), Method: method}).Error
	if db.IsDuplicateKeyError(err) {
		return ErrProofConsumed
	}
	return err
}

// IsProofConsumed reports whether a claim proof was already used: a read-only
// pre-check. The commit's own ConsumeProofTx decides a race between two calls.
func (s Store) IsProofConsumed(eventID string) (bool, error) {
	var n int64
	err := s.DB.Model(&db.CashConsumedProof{}).Where("event_id = ?", eventID).Count(&n).Error
	return n > 0, err
}

// ReleaseProof forgets a consumed proof whose call did not happen, so the caller's
// legitimate retry is not refused.
func (s Store) ReleaseProof(eventID string) error {
	return s.DB.Where("event_id = ?", eventID).Delete(&db.CashConsumedProof{}).Error
}

// lockRecordsOf locks every record naming walletAppID, in tx.
func lockRecordsOf(tx *gorm.DB, walletAppID uint) ([]db.CashRequestRecord, error) {
	var recs []db.CashRequestRecord
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id IN (?)", tx.Model(&db.CashRequestRecordBill{}).Select("record_id").Where("wallet_app_id = ?", walletAppID)).
		Find(&recs).Error
	return recs, err
}
