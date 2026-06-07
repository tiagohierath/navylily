-- Navy Lily — server-side login sessions (pilcrowonpaper-style opaque tokens).
-- Opaque server-side sessions, applied as a Supabase CLI migration.
--
-- Authentication credentials (email + password, confirmation/reset emails) stay
-- in Supabase Auth (GoTrue / auth.users). This table holds OUR OWN sessions on
-- top of GoTrue, so we control them: a session is an opaque "<id>.<secret>"
-- token; we store only the SHA-256 hash of the secret (hex), compare it in
-- constant time, and can revoke any session by deleting its row. The Go server
-- never puts a GoTrue bearer token in the browser cookie anymore.

create table if not exists public.auth_sessions (
  id           text primary key,            -- random, public half of the token
  user_id      uuid        not null,        -- auth.users.id of the signed-in user
  email        text        not null,        -- denormalized: membership is keyed by email
  secret_hash  text        not null,        -- hex SHA-256 of the secret; never the secret itself
  created_at   timestamptz not null default now(),
  last_seen_at timestamptz not null default now()
);

create index if not exists idx_auth_sessions_user on public.auth_sessions(user_id);

-- Same lockdown as members/payment_events: only the service_role key (the Go
-- server) may read/write. RLS on + no policies = deny all except service_role,
-- which bypasses RLS.
alter table public.auth_sessions enable row level security;
