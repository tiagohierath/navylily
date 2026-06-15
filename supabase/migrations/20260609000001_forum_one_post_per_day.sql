-- One post per author per day in the community.
--
-- Enforced as a unique index rather than an application check so it's atomic (no
-- race between "have they posted today?" and the insert) and self-cleaning:
-- because a unique index only indexes live rows, deleting the day's post frees
-- the slot and the member can post again.
--
-- The day is the Brazil calendar day. AT TIME ZONE with a fixed interval (not a
-- zone *name*) is IMMUTABLE, which an index expression requires; Brazil dropped
-- daylight saving in 2019, so the fixed -03:00 is exact. author_email is stored
-- lower-cased by the Go layer, so no extra normalisation is needed here.
-- Partial: only rows from the limit's launch day onward. Posts made before the
-- rule existed (there are legitimate same-day pairs) stay untouched, and the
-- index still blocks any new second-post-of-the-day.
create unique index if not exists uq_forum_posts_author_day
  on public.forum_posts (
    author_email,
    ((created_at at time zone interval '-03:00')::date)
  )
  where created_at >= timestamptz '2026-06-09 00:00:00-03';
