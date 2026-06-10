-- Community forum: short text+image posts with one level of comments.
--
-- Reads are public; writes (post/comment/delete) are gated in the Go layer, which
-- is the only thing that talks to PostgREST (with the service-role key). So, like
-- every other table here, RLS is enabled with NO policies: deny-by-default, the
-- service role bypasses it. Images live on disk (see POST_IMAGE_DIR), not in the
-- database — the row only records that one exists (has_image).

create table if not exists public.forum_posts (
  id           bigint generated always as identity primary key,
  author_email text        not null,
  board        text        not null default 'feedback',  -- one board for now; the column lets more open later
  body         text        not null,                     -- markdown; the first line/heading is the title
  has_image    boolean     not null default false,
  created_at   timestamptz not null default now()
);
create index if not exists idx_forum_posts_board on public.forum_posts (board, id desc);
alter table public.forum_posts enable row level security;

create table if not exists public.forum_comments (
  id           bigint generated always as identity primary key,
  post_id      bigint      not null references public.forum_posts(id) on delete cascade,
  author_email text        not null,
  body         text        not null,
  created_at   timestamptz not null default now()
);
create index if not exists idx_forum_comments_post on public.forum_comments (post_id, id);
alter table public.forum_comments enable row level security;

-- Read views join the author's PUBLIC profile (handle + avatar flags) and a
-- comment count, and NEVER expose author_email — the same privacy rule the public
-- @username API follows. The Go server reads these with the service-role key;
-- the delete handlers read author_email from the base tables to authorize.
create or replace view public.forum_feed as
  select p.id,
         p.board,
         p.body,
         p.has_image,
         p.created_at,
         pr.username          as author_username,
         pr.has_avatar        as author_has_avatar,
         pr.avatar_updated_at as author_avatar_updated_at,
         (select count(*) from public.forum_comments c where c.post_id = p.id) as comment_count
    from public.forum_posts p
    left join public.profiles pr on pr.email = p.author_email;

create or replace view public.forum_comments_view as
  select c.id,
         c.post_id,
         c.body,
         c.created_at,
         pr.username          as author_username,
         pr.has_avatar        as author_has_avatar,
         pr.avatar_updated_at as author_avatar_updated_at
    from public.forum_comments c
    left join public.profiles pr on pr.email = c.author_email;
