-- Phase 7: unified reservation ledger for LSPS1 orders and LSPS2 buy
-- registrations. See BACKLOG.md "Phase 7" (F-15..F-24, T7-1..T7-14) and the
-- linked design artifact for the full rationale.
--
-- Deliberately NOT one `ADD COLUMN ... NOT NULL DEFAULT now()` (second-pass
-- finding): that forces Postgres to rewrite the whole table under an
-- ACCESS EXCLUSIVE lock, since now() isn't a true constant. Nullable add,
-- then a plain UPDATE (ROW EXCLUSIVE, doesn't block reads) to backfill,
-- then SET NOT NULL. A single UPDATE (not a Go-driven batched loop) is used
-- here given this system's realistic table sizes; revisit with batching if
-- a deployment's lsps1.orders/lsps2.buy_registrations ever grows large
-- enough for a single UPDATE's lock duration to matter.

-- ---------------------------------------------------------------------
-- lsps1.orders
-- ---------------------------------------------------------------------

ALTER TABLE lsps1.orders
  ADD COLUMN expires_at timestamptz,
  ADD COLUMN reserved_amount_loki bigint;

-- Backfill: expires_at mirrors the exact 24h window the old hardcoded SQL
-- (ExpireStaleOrders/GetReservedLiquidity, F-19) already enforced for every
-- existing row -- this migration doesn't change past behavior, only makes
-- it explicit and per-row. reserved_amount_loki approximates the real
-- committed capacity (lsp_balance + client_balance); AdditionalChannelCapacity
-- isn't known per historical row (it's a config value, not persisted), and
-- omitting it here only affects orders old enough to already be in a
-- terminal state (the live reserved-liquidity sum only ever reads
-- order_state='CREATED' rows).
UPDATE lsps1.orders
  SET expires_at = created_at + interval '24 hours',
      reserved_amount_loki = lsp_balance_loki + client_balance_loki
  WHERE expires_at IS NULL;

ALTER TABLE lsps1.orders
  ALTER COLUMN expires_at SET NOT NULL,
  ALTER COLUMN reserved_amount_loki SET NOT NULL;

CREATE INDEX orders_active_expiry_idx
  ON lsps1.orders (expires_at) WHERE order_state = 'CREATED';

-- lsps1.orders never had DB-level enforcement of its state machine (only
-- the Go-level Lsps1OrderState/Lsps1PaymentState consts). Added here rather
-- than leaving a fresh asymmetry against lsps2.buy_registrations' new
-- `state` CHECK below. NOT VALID + VALIDATE so any historically-drifted
-- row is caught explicitly instead of silently blocking this migration.
ALTER TABLE lsps1.orders
  ADD CONSTRAINT orders_order_state_check
    CHECK (order_state IN ('CREATED','PAID','COMPLETED','FAILED','EXPIRED')) NOT VALID,
  ADD CONSTRAINT orders_payment_state_check
    CHECK (payment_state IN ('EXPECT_PAYMENT','HOLD','PAID','REFUNDED','EXPIRED','FAILED')) NOT VALID;
ALTER TABLE lsps1.orders VALIDATE CONSTRAINT orders_order_state_check;
ALTER TABLE lsps1.orders VALIDATE CONSTRAINT orders_payment_state_check;

-- ---------------------------------------------------------------------
-- lsps2.buy_registrations
-- ---------------------------------------------------------------------

ALTER TABLE lsps2.buy_registrations
  ADD COLUMN expires_at timestamptz,
  ADD COLUMN reserved_amount_loki bigint,
  ADD COLUMN created_at timestamptz NOT NULL DEFAULT now(), -- true constant: single-step is fine
  ADD COLUMN state varchar NOT NULL DEFAULT 'ACTIVE';        -- true constant: single-step is fine

-- Backfill: generous 24h window from "now" (not the unknown real creation
-- time) so no in-flight registration is retroactively marked expired the
-- moment this migration lands; reserved_amount_loki approximates from the
-- payment size alone (padding/BOLT2-reserve terms aren't known for
-- historical rows -- only new rows written via RequiredCapacityLoki get the
-- accurate figure).
UPDATE lsps2.buy_registrations
  SET expires_at = now() + interval '24 hours',
      reserved_amount_loki = COALESCE(payment_size_mloki, 0) / 1000
  WHERE expires_at IS NULL;

ALTER TABLE lsps2.buy_registrations
  ALTER COLUMN expires_at SET NOT NULL,
  ALTER COLUMN reserved_amount_loki SET NOT NULL,
  ADD CONSTRAINT buy_reg_state_check CHECK (state IN ('ACTIVE','OPENING','CONSUMED','EXPIRED'));

-- Same predicate the live queries (reserved-liquidity sum, cleanup sweep)
-- actually filter on -- OPENING counts as "live" for both: a registration
-- mid-channel-open still commits real (about-to-be-spent) capital and must
-- not be swept out from under an in-flight open (F-15/T7-2).
CREATE INDEX buy_reg_active_expiry_idx
  ON lsps2.buy_registrations (expires_at) WHERE state IN ('ACTIVE','OPENING');

-- T7-7's per-peer standing cap filters by peer_id too; the single-column
-- index above won't serve that COUNT efficiently.
CREATE INDEX buy_reg_peer_active_idx
  ON lsps2.buy_registrations (peer_id, expires_at) WHERE state IN ('ACTIVE','OPENING');

-- ---------------------------------------------------------------------
-- lsps2.bought_channels -- finally written to by T7-2; needs a timestamp
-- to answer "when did this quote turn into a real channel" (R-5).
-- ---------------------------------------------------------------------

ALTER TABLE lsps2.bought_channels
  ADD COLUMN completed_at timestamptz NOT NULL DEFAULT now();

-- ---------------------------------------------------------------------
-- lsps2.promises (T7-9): provisioned in the initial schema, never read or
-- written by any Go code (confirmed by the Phase 7 review -- grep across
-- the tree for "lsps2.promises" turns up only this migration file). The
-- security property it looked like it was for (stopping the same quote
-- being redeemed more than once) is now actually covered at the
-- registration level instead (F-15's fix, T7-2's ACTIVE->OPENING->CONSUMED
-- lifecycle) -- a promise legitimately backs many `get_info` menu reads and
-- doesn't need its own single-use tracking. Dropping rather than leaving
-- unused schema that reads as "this must be enforcing something."
DROP TABLE lsps2.promises;
