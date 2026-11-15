-- T8-2: each node's NIP-LSP announcement status, set from the dashboard
-- (Pause/Resume/Unlist) and read by the announcement publisher. Runtime
-- state, deliberately not in nodes.yaml. A node with no row is open.
CREATE TABLE public.announce_state (
    node_id bytea NOT NULL,
    status text NOT NULL,
    updated_at bigint NOT NULL, -- unix microseconds
    CONSTRAINT announce_state_pkey PRIMARY KEY (node_id),
    CONSTRAINT announce_state_status_check CHECK (status IN ('open', 'paused', 'closed'))
);
