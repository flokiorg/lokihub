ALTER TABLE public.notification_subscriptions ADD COLUMN transport VARCHAR(10) NOT NULL DEFAULT 'push';
