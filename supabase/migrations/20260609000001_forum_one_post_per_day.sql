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
create unique index if not exists uq_forum_posts_author_day
  on public.forum_posts (
    author_email,
    ((created_at at time zone interval '-03:00')::date)
  );
