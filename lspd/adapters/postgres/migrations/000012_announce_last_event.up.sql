-- Publisher state that must survive a restart, for the newest event:
--   last_created_at: the created_at (unix seconds) of the newest event
--     signed for the node; the next one is signed strictly after it, so
--     relays never keep an older event on a created_at tie (NIP-01) and a
--     clock that went back does not make a new event lose to the old one.
--   last_fields: the announced values (niplsp Fields as JSON) of the last
--     event a relay accepted, so closed can be published from them when
--     the config can no longer build the announcement (announce block
--     removed, addrs cleared, LSPS answers that no longer build).
ALTER TABLE public.announce_state
  ADD COLUMN last_created_at bigint NOT NULL DEFAULT 0,
  ADD COLUMN last_fields text NOT NULL DEFAULT '';
