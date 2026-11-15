CREATE TABLE public.notification_queue (
    id SERIAL PRIMARY KEY,
    pubkey BYTEA NOT NULL,
    target_url TEXT NOT NULL,
    transport TEXT NOT NULL,
    method TEXT NOT NULL,
    params JSONB NOT NULL,
    status TEXT NOT NULL, -- 'pending', 'processing', 'completed', 'permanent_fail'
    attempts INT DEFAULT 0,
    next_retry_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
);

CREATE INDEX idx_notification_queue_pending ON public.notification_queue (status, next_retry_at) 
WHERE status NOT IN ('completed', 'permanent_fail');
