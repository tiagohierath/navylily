-- Product pivot: the Community is now the product. Posting is FREE (any account
-- with a username), and a paid Navy membership buys GUARANTEED, priority critique
-- on your work — not the right to post. So the feed needs to mark which posts are
-- by paying members: the page shows a badge, and the owner gets a "members first"
-- review queue (the must-review list, ahead of free posts).
--
-- forum_feed grows one boolean, author_is_member: true when the author has an
-- active, unexpired members row (the same rule the active_members view encodes,
-- joined here against the base table to avoid nesting a security_invoker view).
-- Appended LAST in the select list: CREATE OR REPLACE VIEW may only add columns
-- at the end.
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
         (select count(*) from public.forum_post_likes l where l.post_id = p.id) as like_count,
         (m.email is not null) as author_is_member
    from public.forum_posts p
    left join public.profiles pr on pr.email = p.author_email
    left join public.members   m  on m.email = p.author_email
                                 and m.status = 'active'
                                 and m.expires_at > now();
