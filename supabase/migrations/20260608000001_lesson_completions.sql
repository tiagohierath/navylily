-- Navy Lily — per-user lesson completions, the data behind the profile heatmap.
-- Keyed by email like members/profiles, so the Go server records a completion
-- under the session's verified e-mail and the public /@username page can render
-- a GitHub-style "Aulas completadas" calendar.
--
-- One row per (email, lesson): a lesson counts ONCE, on the day it was first
-- completed. Re-marking the same lesson never moves the date or double-counts.
create table if not exists public.lesson_completions (
  email        text not null,
  lesson_slug  text not null,             -- e.g. "001" / "protected/004"
  completed_on date not null default (now() at time zone 'utc')::date,
  created_at   timestamptz not null default now(),
  primary key (email, lesson_slug)
);

-- The heatmap query is "all of one user's completions"; this index serves it and
-- keeps the per-day grouping cheap.
create index if not exists idx_lesson_completions_email_day
  on public.lesson_completions (email, completed_on);

-- Same lockdown as members/profiles/auth_sessions: RLS on, no policies => only
-- the service_role key (the Go server) may read/write. No public/anon access;
-- the public heatmap is exposed through the Go API, which returns day counts
-- only — never the e-mail or which lessons were taken.
alter table public.lesson_completions enable row level security;
