-- Navy Lily — remap completions for the four lessons that moved from the paid
-- course into the free course.
--
-- The paid lessons were served at /protected/00N.html, so their completions
-- were keyed "protected/001".."protected/004" (the lesson_slug is derived from
-- the URL path). They now live in the free sequence at /008.html../011.html,
-- keyed "008".."011". Without this remap, members who'd already finished those
-- lessons see them as not-done on the lists/heatmap, and "continuar de onde
-- parou" can send them back into lessons they've already read.
--
-- Mapping (paid order -> new free order):
--   protected/001 -> 008
--   protected/002 -> 009
--   protected/003 -> 010
--   protected/004 -> 011
--
-- Insert-then-delete (rather than a plain UPDATE) so a member who completed
-- BOTH the old paid slug and, after the lessons went free, the new free slug
-- doesn't trip the (email, lesson_slug) primary key: ON CONFLICT DO NOTHING
-- keeps the existing free-slug row, and the DELETE drops the redundant paid
-- rows. The original completed_on/created_at ride along, so the heatmap dates
-- don't shift. Re-running is a no-op (no protected/* rows remain to move).

insert into public.lesson_completions (email, lesson_slug, completed_on, created_at)
select lc.email, m.new_slug, lc.completed_on, lc.created_at
from public.lesson_completions lc
join (values
  ('protected/001', '008'),
  ('protected/002', '009'),
  ('protected/003', '010'),
  ('protected/004', '011')
) as m(old_slug, new_slug) on lc.lesson_slug = m.old_slug
on conflict (email, lesson_slug) do nothing;

delete from public.lesson_completions
where lesson_slug in ('protected/001', 'protected/002', 'protected/003', 'protected/004');
