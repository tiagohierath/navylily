-- Navy Lily — silence the Supabase advisor warning about the leftover
-- public.rls_auto_enable() function being executable by anon/authenticated.
-- Guarded so it's a no-op if the function was never created (fresh installs).

do $$
begin
  if exists (
    select 1 from pg_proc p
    join pg_namespace n on n.oid = p.pronamespace
    where n.nspname = 'public' and p.proname = 'rls_auto_enable'
  ) then
    revoke execute on function public.rls_auto_enable() from anon, authenticated, public;
  end if;
end $$;
