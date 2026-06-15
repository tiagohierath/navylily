-- Relax the community posting limit from 1/day to a few per day.
--
-- The hard 1-per-day rule was a unique index (uq_forum_posts_author_day). A
-- "N per day" cap can't be a unique index, so the limit moves to the application
-- layer (a count of today's posts vs dailyPostLimit in main.go). Drop the index
-- so the 2nd..Nth post of the day is no longer rejected by the database.
drop index if exists public.uq_forum_posts_author_day;
