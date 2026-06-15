-- Likes ("curtidas") on community posts and comments.
--
-- Same access model as the rest of the forum: RLS on with NO policies (deny by
-- default) and only the Go layer writing via the service role. One row per
-- (target, account) — the primary key makes a double-like impossible, and
-- unliking is just deleting the row. user_email never leaves the views; only
-- counts do.

create table if not exists public.forum_post_likes (
  post_id    bigint      not null references public.forum_posts(id) on delete cascade,
  user_email text        not null,
  created_at timestamptz not null default now(),
  primary key (post_id, user_email)
);
alter table public.forum_post_likes enable row level security;

create table if not exists public.forum_comment_likes (
  comment_id bigint      not null references public.forum_comments(id) on delete cascade,
  user_email text        not null,
  created_at timestamptz not null default now(),
  primary key (comment_id, user_email)
);
alter table public.forum_comment_likes enable row level security;

-- The read views grow a like_count. Appended LAST in the select list:
-- CREATE OR REPLACE VIEW may only add columns at the end.
create or replace view public.forum_feed as
  select p.id,
         p.board,
         p.body,
         p.has_image,
         p.created_at,
         pr.username          as author_username,
         pr.has_avatar        as author_has_avatar,
         pr.avatar_updated_at as author_avatar_updated_at,
         (select count(*) from public.forum_comments c where c.post_id = p.id) as comment_count,
         (select count(*) from public.forum_post_likes l where l.post_id = p.id) as like_count
    from public.forum_posts p
    left join public.profiles pr on pr.email = p.author_email;

create or replace view public.forum_comments_view as
  select c.id,
         c.post_id,
         c.body,
         c.created_at,
         pr.username          as author_username,
         pr.has_avatar        as author_has_avatar,
         pr.avatar_updated_at as author_avatar_updated_at,
         (select count(*) from public.forum_comment_likes l where l.comment_id = c.id) as like_count
    from public.forum_comments c
    left join public.profiles pr on pr.email = c.author_email;
