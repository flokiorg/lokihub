-- S-5: a channel point (funding_txid, funding_outpoint) must belong to at
-- most one LSPS1 order. Without this, two orders can concurrently read the
-- same LND channel as "unclaimed" (findUnclaimedChannelPoint's read is not
-- atomic with the later UpdateLsps1OrderFunding write) and both attribute
-- it to themselves. The partial index only covers rows that actually have
-- funding recorded, matching how funding_txid is used elsewhere (NULL or
-- '' both mean "not yet funded").
CREATE UNIQUE INDEX lsps1_orders_funding_point_idx ON lsps1.orders (funding_txid, funding_outpoint)
WHERE funding_txid IS NOT NULL AND funding_txid <> '';
