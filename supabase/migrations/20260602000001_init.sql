-- Navy Lily — auth/payments schema for self-hosted Supabase (Postgres)
-- Run this ONCE in Supabase Studio -> SQL Editor.
-- Authentication itself is handled by Supabase Auth (GoTrue / auth.users).
-- These tables only track PAID MEMBERSHIP, keyed by the buyer's email so the
-- AbacatePay webhook (and the PIX status poll) can grant access.

-- A paid member. One row per email that bought (or was granted) access.
create table if not exists public.members (
  email             text primary key,
  name              text,
  status            text        not null default 'active'
                      check (status in ('active', 'refunded', 'expired', 'canceled')),
  abacate_charge_id text,
  source            text        not null default 'abacatepay',
  started_at        timestamptz not null default now(),
  -- 197 BRL / year subscription -> access lasts a year from purchase.
  expires_at        timestamptz not null default (now() + interval '1 year'),
  updated_at        timestamptz not null default now()
);

-- Raw audit log of every AbacatePay webhook we accept (idempotency + debugging).
create table if not exists public.payment_events (
  id            bigint generated always as identity primary key,
  charge_id     text,
  event_type    text,
  charge_status text,
  email         text,
  received_at   timestamptz not null default now(),
  payload       jsonb       not null
);

create index if not exists idx_payment_events_charge on public.payment_events(charge_id);

-- Lock both tables down: only the service_role key (used by the Go server)
-- may read/write. No public/anon access. RLS on + no policies = deny all
-- except service_role, which bypasses RLS.
alter table public.members        enable row level security;
alter table public.payment_events enable row level security;

-- Convenience: a member is "active" only while not refunded/canceled and unexpired.
-- The Go server checks this, but the view documents the rule in one place.
create or replace view public.active_members as
  select email, name, expires_at
  from public.members
  where status = 'active' and expires_at > now();
