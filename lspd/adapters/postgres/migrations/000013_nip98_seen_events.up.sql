-- FLSPD-0016 / R-LSP-NIP98REPLAY: durable, shared NIP-98 replay cache for the
-- operator dashboard's HTTP auth. The in-process cache could not stop a
-- captured in-window auth event from being replayed against a freshly
-- restarted process (empty cache) or a second replica (its own cache). A row
-- here records a seen event id until expires_at (a few minutes, past the auth
-- freshness window); a replay collides on the primary key and is rejected.
CREATE TABLE public.nip98_seen_events (
    event_id text NOT NULL,
    expires_at bigint NOT NULL, -- unix seconds; prune target
    CONSTRAINT nip98_seen_events_pkey PRIMARY KEY (event_id)
);

CREATE INDEX nip98_seen_events_expires_at_idx ON public.nip98_seen_events (expires_at);
