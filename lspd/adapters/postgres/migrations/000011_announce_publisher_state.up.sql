-- Publisher state that must survive a restart:
--   last_published_at: when any announcement of the node was last accepted
--     by a relay; a node that never published has nothing to withdraw.
--   off_closed_published_at: when status closed was published because
--     announcing was switched off; cleared when it is switched on again.
--   self_check_block: why a failed self-check stopped publishing ('' when
--     not blocked); only the dashboard's Recheck clears it.
ALTER TABLE public.announce_state
  ADD COLUMN last_published_at bigint,
  ADD COLUMN off_closed_published_at bigint,
  ADD COLUMN self_check_block text NOT NULL DEFAULT '';
