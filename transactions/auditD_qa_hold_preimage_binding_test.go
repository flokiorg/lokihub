package transactions

// AUDIT-D QA, follow-up to D-QA-3: exercise tests.MockLn's hold-invoice
// preimage binding.
//
// The mock gained HoldInvoiceBindsPreimage when D-QA-3 was fixed, and an opt-in
// capability nothing opts into is the same defect this round keeps finding — a
// flag whose behaviour no test has ever observed is indistinguishable from one
// that does not work. (The flag it was modelled on, MakeInvoiceHonoursAmount, is
// set nowhere in the repository to this day.) So this file is the one caller,
// and it asserts the flag in both directions rather than merely switching it on.
//
// What the binding buys that the argument recording does not: recording catches
// the hub handing the node a preimage different from the one it recorded, which
// is D-QA-3's own mutation. This catches the hub settling a preimage for an
// invoice THIS NODE NEVER ISSUED — a real node would reject that, and before the
// binding existed the mock accepted every preimage unconditionally, so a hub
// that settled against a foreign or stale invoice looked identical to one that
// did not.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/tests"
)

func auditDQAPreimageFor(b byte) (preimage, paymentHash string) {
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = b
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(raw), hex.EncodeToString(sum[:])
}

func TestAuditDQA_MockHoldInvoiceBinding_RefusesAPreimageTheNodeNeverIssued(t *testing.T) {
	ln, err := tests.NewMockLn()
	require.NoError(t, err)
	ln.HoldInvoiceBindsPreimage = true

	issuedPreimage, issuedHash := auditDQAPreimageFor(0xa7)
	foreignPreimage, _ := auditDQAPreimageFor(0xb9)

	ctx := context.Background()

	// Nothing issued yet: the binding stays out of the way, because a suite that
	// drives the settle path without issuing an invoice first has nothing to match
	// against and failing it would be an artefact of the fixture, not a finding.
	require.NoError(t, ln.SettleHoldInvoice(ctx, foreignPreimage),
		"with no invoice issued through this mock the binding must not fire")

	issued, err := ln.MakeHoldInvoice(ctx, 2000, "auditD binding", "", 3600, issuedHash)
	require.NoError(t, err)
	require.Equal(t, issuedHash, issued.PaymentHash,
		"the mock must issue the invoice it was asked for, which is what the binding then matches against")

	// The preimage whose sha256 is the issued hash settles.
	require.NoError(t, ln.SettleHoldInvoice(ctx, issuedPreimage))

	// One for an invoice this node never issued does not.
	err = ln.SettleHoldInvoice(ctx, foreignPreimage)
	require.Error(t, err,
		"settling a preimage for an invoice this node never issued must be refused once an "+
			"invoice HAS been issued — a real node has no HTLC to release")
	require.Contains(t, err.Error(), "no accepted hold invoice")

	// And every attempt is still recorded, including the refused one: what the hub
	// ASKED for is the interesting part, and a call that errors is the most
	// interesting of all.
	require.Equal(t,
		[]string{foreignPreimage, issuedPreimage, foreignPreimage},
		ln.SettleHoldPreimages())
}

// TestAuditDQA_MockHoldInvoiceBinding_IsOffByDefault pins the opt-in half. If it
// ever became the default, suites that settle without issuing would fail on the
// fixture's shape rather than on a defect — which is why the flag exists instead
// of the behaviour being unconditional.
func TestAuditDQA_MockHoldInvoiceBinding_IsOffByDefault(t *testing.T) {
	ln, err := tests.NewMockLn()
	require.NoError(t, err)
	require.False(t, ln.HoldInvoiceBindsPreimage, "the binding must be opt-in")

	ctx := context.Background()
	_, issuedHash := auditDQAPreimageFor(0xc3)
	_, err = ln.MakeHoldInvoice(ctx, 2000, "auditD binding", "", 3600, issuedHash)
	require.NoError(t, err)

	foreignPreimage, _ := auditDQAPreimageFor(0xd5)
	require.NoError(t, ln.SettleHoldInvoice(ctx, foreignPreimage),
		"with the binding off, an unknown preimage must still be accepted — existing suites rely on it")
}
