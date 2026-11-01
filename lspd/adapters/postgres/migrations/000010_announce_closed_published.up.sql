-- T8-3: when the publisher announced status closed for the node's current
-- Unlist. Unlist publishes closed once, then stops; this keeps "once" true
-- across restarts. Cleared whenever the status changes again.
ALTER TABLE public.announce_state
  ADD COLUMN closed_published_at bigint; -- unix microseconds, NULL until published
