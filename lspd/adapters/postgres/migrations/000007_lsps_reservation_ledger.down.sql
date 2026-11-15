CREATE TABLE lsps2.promises (
    promise varchar PRIMARY KEY,
    token varchar NOT NULL,
    valid_until varchar NOT NULL
);
CREATE INDEX idx_lsps2_promises_valid_until ON lsps2.promises (valid_until);

ALTER TABLE lsps2.bought_channels
  DROP COLUMN completed_at;

DROP INDEX lsps2.buy_reg_peer_active_idx;
DROP INDEX lsps2.buy_reg_active_expiry_idx;

ALTER TABLE lsps2.buy_registrations
  DROP CONSTRAINT buy_reg_state_check,
  DROP COLUMN state,
  DROP COLUMN created_at,
  DROP COLUMN reserved_amount_loki,
  DROP COLUMN expires_at;

ALTER TABLE lsps1.orders
  DROP CONSTRAINT orders_payment_state_check,
  DROP CONSTRAINT orders_order_state_check;

DROP INDEX lsps1.orders_active_expiry_idx;

ALTER TABLE lsps1.orders
  DROP COLUMN reserved_amount_loki,
  DROP COLUMN expires_at;
