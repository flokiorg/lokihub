package apps

import (
	"time"

	"github.com/ohstr/nmilat/nipCO"
	"github.com/ohstr/nmilat/nipcash"
	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/db"
)

// unconfirmedMaxWait is how long an unconfirmed order payment keeps blocking
// the deletion of its Cash Hub after it was last seen.
const unconfirmedMaxWait = int64(7 * 24 * 3600)

// OrderRetentionTx is what decides how long order o is kept (NIP-CO §Reading an
// Order) and whether it blocks deleting its Cash Hub.
func OrderRetentionTx(tx *gorm.DB, o *db.CashOrder, horizon int64, now time.Time) (nipCO.OrderRetention, nipCO.UnmintedPayments, error) {
	r := nipCO.OrderRetention{Payment: o.Payment, PayBy: o.PayBy, Deadline: o.Deadline, Horizon: horizon, Watched: true}
	var u nipCO.UnmintedPayments
	var payments []db.CashOrderPayment
	if err := tx.Where("order_id = ?", o.ID).Find(&payments).Error; err != nil {
		return r, u, err
	}
	for _, p := range payments {
		switch p.State {
		case db.CashOrderPaymentConfirmed:
			r.Seen = true
			u.Confirmed++
		case db.CashOrderPaymentSeen:
			r.Seen = true
			u.Unconfirmed++
			u.LastSeenAt = max(u.LastSeenAt, p.SeenAt)
		case db.CashOrderPaymentMinted, db.CashOrderPaymentReorged, db.CashOrderPaymentKept, db.CashOrderPaymentDropped:
			r.Seen = true
		}
	}
	u.MaxWait = unconfirmedMaxWait
	if o.Payment == nipCO.PaymentLightning {
		live, err := billsLiveOrTombstoned(tx, o, now)
		if err != nil {
			return r, u, err
		}
		r.BillLiveOrTombstoned = live
	}
	return r, u, nil
}

// billsLiveOrTombstoned reports whether a bill the order minted is live or
// inside its tombstone window.
func billsLiveOrTombstoned(tx *gorm.DB, o *db.CashOrder, now time.Time) (bool, error) {
	var bills []db.CashRequestRecordBill
	if err := tx.Joins("JOIN cash_request_records ON cash_request_records.id = cash_request_record_bills.record_id").
		Where("cash_request_records.request_id = ?", o.RequestID).Find(&bills).Error; err != nil {
		return false, err
	}
	for _, b := range bills {
		var live int64
		if err := tx.Model(&db.App{}).Where("id = ?", b.WalletAppID).Count(&live).Error; err != nil {
			return false, err
		}
		if live > 0 {
			return true, nil
		}
		if tomb, ok := db.SpentBillTombstoneFor(tx, b.WalletPubkey); ok && db.RetentionWindowOpen(tomb.RetainedUntil, now) {
			return true, nil
		}
	}
	return false, nil
}

// DropOrderTx deletes an order, its record and its record's bill rows, its
// used request_id mark when dropMark, and burns its address.
func DropOrderTx(tx *gorm.DB, o *db.CashOrder, dropMark bool) error {
	if o.Address != nil {
		if err := tx.Model(&db.CashOrderAddress{}).Where("address = ?", *o.Address).Update("burnt", true).Error; err != nil {
			return err
		}
	}
	if err := tx.Where("request_id = ? AND method = ?", o.RequestID, nipcash.MethodCashOrder).Delete(&db.CashRequestRecord{}).Error; err != nil {
		return err
	}
	if dropMark {
		if err := tx.Where("request_id = ?", o.RequestID).Delete(&db.CashUsedRequestID{}).Error; err != nil {
			return err
		}
	}
	return tx.Delete(&db.CashOrder{}, o.ID).Error
}

// HubOrderBlockersTx counts a Cash Hub's orders that keep it from being deleted
// (nipCO.OrderBlocksHubDelete: an order kept only to watch its address does
// not) and the earliest time they all stop, ok false while one has no such
// time yet.
func HubOrderBlockersTx(tx *gorm.DB, hubID uint, horizon int64, now time.Time) (count int64, at int64, ok bool, err error) {
	var orders []db.CashOrder
	if err := tx.Where("hub_app_id = ?", hubID).Find(&orders).Error; err != nil {
		return 0, 0, false, err
	}
	ok = true
	for i := range orders {
		r, u, err := OrderRetentionTx(tx, &orders[i], horizon, now)
		if err != nil {
			return 0, 0, false, err
		}
		if !nipCO.OrderBlocksHubDelete(r, u, now.Unix()) {
			continue
		}
		count++
		t, known := nipCO.OrderHubDeleteAt(r, u)
		if !known {
			ok = false
			continue
		}
		at = max(at, t)
	}
	return count, at, ok, nil
}

// DeleteHubOrdersTx deletes a Cash Hub's orders and their records with the Hub.
// Their addresses stay: a later payment to one is kept, credited to no app.
func DeleteHubOrdersTx(tx *gorm.DB, hubID uint) error {
	var orders []db.CashOrder
	if err := tx.Where("hub_app_id = ?", hubID).Find(&orders).Error; err != nil {
		return err
	}
	for i := range orders {
		if err := DropOrderTx(tx, &orders[i], false); err != nil {
			return err
		}
	}
	return tx.Model(&db.CashOrderPayment{}).Where("hub_app_id = ?", hubID).Update("order_id", nil).Error
}
