-- active_members is created SECURITY DEFINER by default (it runs as its
-- creator, bypassing the caller's RLS). Switch it to security_invoker so it
-- enforces the querying role's RLS — silences the Supabase advisor and is the
-- safer default. Postgres 15+ / Supabase supports this.
alter view public.active_members set (security_invoker = on);
