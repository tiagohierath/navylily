-- Reply-to-comment: a comment can optionally reference a parent comment.
-- When the parent is deleted its replies cascade away via the FK.
-- CREATE OR REPLACE VIEW may only append columns — new ones go at the end.

alter table public.forum_comments
  add column if not exists parent_comment_id bigint
    references public.forum_comments(id) on delete cascade;

create index if not exists idx_forum_comments_parent
  on public.forum_comments (parent_comment_id);

create or replace view public.forum_comments_view as
  select c.id,
         c.post_id,
         c.body,
         c.created_at,
         pr.username          as author_username,
         pr.has_avatar        as author_has_avatar,
         pr.avatar_updated_at as author_avatar_updated_at,
         (select count(*) from public.forum_comment_likes l where l.comment_id = c.id) as like_count,
         c.parent_comment_id,
         parent_pr.username   as reply_to_username
    from public.forum_comments c
    left join public.profiles pr on pr.email = c.author_email
    left join public.forum_comments pc on pc.id = c.parent_comment_id
    left join public.profiles parent_pr on parent_pr.email = pc.author_email;
