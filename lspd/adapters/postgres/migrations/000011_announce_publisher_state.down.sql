ALTER TABLE public.announce_state
  DROP COLUMN self_check_block,
  DROP COLUMN off_closed_published_at,
  DROP COLUMN last_published_at;
