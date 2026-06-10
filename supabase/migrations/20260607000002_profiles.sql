-- Navy Lily — user profiles (display username, bio, avatar).
-- Keyed by email, same as public.members, so the Go server can join the two by
-- the session's verified e-mail. Avatar BYTES live on the server filesystem
-- (see AVATAR_DIR); this table only tracks that one exists + when it changed,
-- plus the username (limited edits) and a short bio.
create table if not exists public.profiles (
  email             text primary key,
  -- Display name. Lowercase [a-z0-9_], 3-20 chars. NULL until first chosen.
  username          text,
  -- How many times the username has CHANGED after its initial choice. The first
  -- pick is free; after that the Go server allows up to 3 changes (lifetime).
  username_changes  int         not null default 0,
  bio               text        not null default '',
  has_avatar        boolean     not null default false,
  -- Bumped on each avatar upload so the UI can cache-bust /avatar/me.
  avatar_updated_at timestamptz,
  created_at        timestamptz not null default now(),
  updated_at        timestamptz not null default now()
);

-- Usernames are unique, case-insensitively. Partial so unset (NULL) rows don't
-- collide with each other.
create unique index if not exists idx_profiles_username_lower
  on public.profiles (lower(username))
  where username is not null;

-- Lock down like members/payment_events: RLS on, no policies => only the
-- service_role key (the Go server) may read/write. No public/anon access.
alter table public.profiles enable row level security;
