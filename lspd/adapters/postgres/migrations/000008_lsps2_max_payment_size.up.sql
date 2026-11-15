-- F-22 (redemption-time half, found in a later review pass): lsps2.buy
-- computes a worst-case payment-size bound (the client's own
-- payment_size_mloki if given, else the quote's own max_payment_size_mloki)
-- to size the buy-time reservation, but never persisted that bound --
-- resolvePayment's variable-size-mode redemption path forwarded whatever
-- the real incoming HTLC amount was, checked only against an unrelated,
-- node-wide ceiling (JitMaxPaymentSizeMloki), not this registration's own
-- negotiated max. A real channel could therefore be opened larger than
-- what the reserved-liquidity ledger believed was committed.
--
-- Nullable, no backfill: existing rows simply have nothing to enforce at
-- redemption (the same "no check" behavior they already had), and this
-- ledger's rows are always short-lived (max TTL 24h), so no row from
-- before this migration should still be live by the time it's deployed.
ALTER TABLE lsps2.buy_registrations
  ADD COLUMN max_payment_size_mloki bigint;
