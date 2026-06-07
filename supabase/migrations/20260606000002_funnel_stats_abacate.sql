-- Navy Lily — refine funnel_stats(): count only members who paid through the new
-- checkout flow (source='abacatepay'), excluding the legacy 'gift'/Kiwify imports,
-- so "Signup -> Paid" is a true conversion of the new funnel rather than a >100%
-- artifact of the one-off migration. Everything else unchanged.
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
         and source = 'abacatepay'
         and expires_at > now()
         and started_at >= now() - (days * interval '1 day'));
$$;

revoke all on function public.funnel_stats(int) from public, anon, authenticated;
grant execute on function public.funnel_stats(int) to service_role;
