-- Navy Lily — funnel_stats(): aggregate counts for the terminal dashboard.
-- Returns signups (auth.users) and active paid members within the last `days`.
-- SECURITY DEFINER so it can read the auth schema; locked to service_role only
-- and returns ONLY aggregate counts, never rows/PII.
create or replace function public.funnel_stats(days int default 30)
returns table (users bigint, paid bigint)
language sql
security definer
set search_path = public, auth
as $$
  select
    (select count(*) from auth.users
       where created_at >= now() - (days * interval '1 day')),
    (select count(*) from public.members
       where status = 'active'
         and expires_at > now()
         and started_at >= now() - (days * interval '1 day'));
$$;

revoke all on function public.funnel_stats(int) from public, anon, authenticated;
grant execute on function public.funnel_stats(int) to service_role;
