# CHECKLIST FOR WEBSITE

Train imagination drawing through real critique, not videos. Join a system where your work is reviewed, corrected, and improved over time.

# CLAUDE, fix this as soon as possible

I have two github repos, private and public, please upload everything important to the github, as backup
do not upload to github the all.txt or substack complete posts, the transcripts and substack export. 

IN ABSOLUTE ORDER, has to be done in this order
read it all before starting anything
(already done on a weaker AI model, so, re-verify)
- make a simple index first note called "Leia isso primeiro" with notes in a good order, clean categories
- post them onto the wiki1, period, public now.
- make them better by removing third person language
- remove quotation marks, using my quotes verbatim is ok, but make it sound like an article I WROTE.
- write the notes with the posts folder from substack, add them to public sphere too, no images, wiki never has images.
for substack, do not rewrite, just categorize and remove fillers like CTAs, links to products, filler stuff, its mostly already really good written.
- mix substack notes and other notes, in a kind of random way.
- add a button to go to random article with dice emoji
- for every wiki lesson, add 50 tags/synonims that people could use to search it, to facilitate search. per example: anatomy = body, legs, arms, etc.
- for every wiki lesson, add bullet points at the end with practical tips you could really really do, like 10 to 20 points.

NOT DONE YET
- put all notes that come from substack above all the others on /wiki page, but, do not put on a separate category, just above the others



## TODO (active)

- [x] WIKI ORDER (DONE 2026-06-15): substack-derived notes listed first on /wiki
      index (flat list, no separate heading), then the rest. Implemented via
      content/WIKI/.substack manifest + two-pass loop in build_wiki (parser.sh).
      19 substack notes detected (untracked WIKI note whose slug matches a real
      posts/ substack post). Falls back to old order if manifest is missing.

- [x] WIKI EXPANSION (DONE 2026-06-15): 85 wiki pages live in public/wiki/. wiki2 notes
      + ~20 substack posts added to content/WIKI/. "Leia isso primeiro" index page
      created. All articles: first-person language, no 3rd-person attribution, 50 search
      tags, "Como aplicar" practical tips section. 🎲 random article button in /wiki.
      ./parser.sh runs clean, no errors.

- [ ] RESTART to apply pending binary + wiki is already live from disk (no restart needed
      for wiki pages)


- [x] Reply-to-comment: DB migration (parent_comment_id), Go API, and frontend "responder" button with inline form + tree rendering in community.html

- [ ] RESTART to go live — binary ALREADY rebuilt (auth/navylily-auth, with the
      payment webhook fixes + board rename below); just restart:
      `sudo systemctl restart navylily.service`
      This activates: the payment webhook hardening (memberShielded fail-closed
      revoke path; log-after-action so a failed grant isn't deduped away — see the
      payment-bugs memory), the "desafios"→"Sketchbooks" board rename (key
      sketchbooks; forum_posts was empty so no rows to migrate), the new
      `/post/<id>` route (until restart, clicking a post 404s since web/ links now
      point at /post/<id>), the new `ABACATE_PRODUCT_ID` (card sub now R$ 497/yr),
      and `PRICE_CENTS=49700` (PIX = R$ 497). Other frontend changes (composer box,
      gate list, board bar, R$ 497, Sketchbooks tab) are already live since web/ is
      served from disk.

- [ ] OPTIONAL — coupon/one-time product still R$ 197. ABACATE_ONETIME_PRODUCT_ID
      (prod_e1K6FqDk6KJFctzpLjHJazBs) is the no-cycle product used only by the
      coupon checkout. Bump to R$ 497 the same way (POST /v2/products/create, no
      cycle) + update auth/.env if the coupon base price should be 497 too.

## DONE 2026-06-14

- [x] DB: 1/day index dropped — migration 20260614000001 is applied on remote
      (verified via `supabase migration list`); 30/day cap now governs.
- [x] 497 on the buy page (comprar.html) and profile upsell.
- [x] PRICE_CENTS=49700 in auth/.env + .env.example (PIX charges R$ 497 after restart).
- [x] Free logged-in AND paid members can post (canPost = account + username).
- [x] Two forum boards: "feedback" + "desafios" (backend forumBoards + tab UI).
- [x] "NAVY" tag replaces the ⚓ badge for paid members in the feed.
- [x] Community composer styled as a bordered writing box (square corners, navy
      palette) + "adicionar imagem" link next to the 📷.
- [x] Community gate/upsell collapsed into one 3-item list (criar conta grátis ·
      virar membro pago p/ prioridade · entrar); removed the old upsell line.
- [x] Board tabs are now one symmetric bar (equal-width segments, navy fill on
      the active tab).
- [x] Post pages — open a post Twitter-style at /post/<id> (route added) or
      ?post=<id>: post + ALL comments + reply form + "← Comunidade" back link;
      feed/composer/boards hidden. Feed links/title/share now point at /post/<id>.
      (Route needs the rebuild+restart above; web/ changes already live.)
- [x] Removed buy-page (comprar.html) lines: "Tudo aqui é gratuito…" lead,
      ", não automáticos" in the perks list, and the "Sem assinar você continua…"
      muted paragraph.
- [x] AbacatePay: created R$ 497/yr ANNUALLY product prod_YgDP0kLtZxgG6BUb53tuaLNP
      and pointed ABACATE_PRODUCT_ID at it (card sub charges 497 after restart).


# RUNNING THE SITE (ops runbook)

The live site at **https://tiagohierath.com** is served entirely from THIS PC by
two systemd services. The data (users, payments) lives in cloud Supabase, so the
PC only runs the app + serves the lesson files. If this PC sleeps/shuts
down/loses internet, the site is down.

- **navylily.service** — the Go auth + payments server. Listens on
  `127.0.0.1:8090`. Binary: `/home/tiago/navylily/auth/navylily-auth`.
  Working dir: `/home/tiago/navylily/auth` (it loads `auth/.env` and resolves
  `web/`, `../public`, `../protected` from there).
- **cloudflared.service** — the Cloudflare Tunnel that exposes localhost:8090 to
  the internet at tiagohierath.com. No router ports are opened.

Request path: visitor → tiagohierath.com → Cloudflare → cloudflared (this PC) →
localhost:8090 → navylily-auth.

## Everyday commands

```bash
# STATUS — is it up?
systemctl status navylily.service cloudflared.service --no-pager
curl -sS -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8090/me   # 200 = app alive

# RESTART (do this after editing auth/.env, or after a rebuild)
sudo systemctl restart navylily.service
sudo systemctl restart cloudflared.service      # only if the tunnel itself is sick

# STOP / START
sudo systemctl stop navylily.service
sudo systemctl start navylily.service

# LOGS (live tail; Ctrl-C to quit)
journalctl -u navylily.service -f
journalctl -u navylily.service --since "15 min ago" --no-pager
journalctl -u cloudflared.service -f

# ENABLE / DISABLE auto-start at boot (already enabled; rarely needed)
sudo systemctl enable navylily.service cloudflared.service    # start on boot
sudo systemctl disable navylily.service cloudflared.service   # don't start on boot
```

## Deploy a code or content change

The server runs a COMPILED binary, so Go/HTML changes need a rebuild + restart.
(Editing files under `public/` or `protected/` is picked up live — no rebuild —
but a restart is harmless.)

```bash
cd /home/tiago/navylily/auth
go build -o navylily-auth .            # compile; fix any errors before restarting
sudo systemctl restart navylily.service
journalctl -u navylily.service -n 20 --no-pager   # confirm it came back up
```

## First-time install of the services (already done; reference only)

```bash
cd /home/tiago/navylily/auth && go build -o navylily-auth .
sudo cp deploy/navylily.service   /etc/systemd/system/navylily.service
sudo cp deploy/cloudflared.service /etc/systemd/system/cloudflared.service
sudo systemctl daemon-reload
sudo systemctl enable --now navylily.service cloudflared.service
```

## Common tasks

```bash
# Grant a member free access (1 year), by e-mail — writes to Supabase:
cd /home/tiago/navylily/auth && ./gift.sh someone@example.com
./gift.sh --dry-run someone@example.com    # preview, writes nothing

# After changing a secret in auth/.env (e.g. ABACATE_WEBHOOK_SECRET,
# API keys, SMTP), you MUST restart for it to take effect:
sudo systemctl restart navylily.service
```

## Gotchas

- **`auth/.env` is read only at startup** — any change there needs a restart.
- **Webhook secret**: AbacatePay's webhook URL is
  `https://tiagohierath.com/webhooks/abacatepay?webhookSecret=<ABACATE_WEBHOOK_SECRET>`.
  That secret is a value YOU choose in auth/.env, not the API key.
- **Email / login redirects** are configured in the **Supabase dashboard**
  (cloud project), not in this repo — see the prod-domain memory note.
- If the site is unreachable but `navylily.service` is active, suspect
  **cloudflared** (tunnel) or the internet connection — check its logs.


---

# MANAGING LESSONS (content workflow)

Lessons are written as **Markdown** and compiled to HTML by `parser.sh` (which
shells out to `pandoc`). You never hand-write the lesson HTML — you edit the
`.md` source and rebuild.

## Where things live

```
content/free/NNN.md   ->  public/NNN.html        (free; open to everyone)
content/paid/NNN.md   ->  protected/NNN.html      (paid; gated by the auth server)
                          public/lessons.html     (auto-generated index of ALL lessons)
template.html         ->  the page shell every lesson is poured into
parser.sh             ->  the build script
```

Two independent sequences, **both starting at `001`**: free and paid. The
filename's number (`003.md`) is also the lesson's order.

## How to add a new lesson

1. **Create the markdown file** in the right folder, named with a zero-padded
   3-digit number — the next free number for a free lesson, etc.:

   ```bash
   cd /home/tiago/navylily
   $EDITOR content/free/010.md      # free lesson #10
   # or
   $EDITOR content/paid/002.md      # paid lesson #2
   ```

2. **Follow the per-file convention** (this is how the parser extracts metadata):

   ```markdown
   # Lesson title goes here          <- line with first "# " becomes the title
   tag1 tag2 tag3                    <- 2nd line = space-separated tags (blanked in output)

   The lesson body in normal Markdown. Images, **bold**, lists, etc.
   Put images in static/ (or public/) and reference them by path.

   002                              <- trailing bare numbers = "related lessons"
   free/005                         <- prefix with free/ or paid/ to cross sequences
   ```

   - **Title**: the first `# ` heading.
   - **Tags**: the *second line* of the file. It's used as metadata and then
     blanked so it isn't rendered in the page body.
   - **Related lessons**: any *trailing* lines that are just a lesson number
     (optionally prefixed `free/` or `paid/`). They render as an "Aulas
     relacionadas" list at the bottom. A bare number resolves inside the same
     collection first; use `free/NNN` or `paid/NNN` to link across.

3. **Build**:

   ```bash
   ./parser.sh
   ```

   This rebuilds **all** lessons (free → `public/`, paid → `protected/`) and
   regenerates `public/lessons.html`. It prints `built: ...` per file and warns
   about any related-lesson reference it couldn't resolve.

4. **Publish.** Files under `public/` and `protected/` are served live by the
   running server — no rebuild of the Go binary needed. A restart is harmless
   but not required:

   ```bash
   curl -sS -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8090/NNN.html
   ```

## Free vs paid — what differs

- **Free** lessons go to `public/` and get the subscription banner
  (`build ... -V banner=1`). Anyone can read them.
- **Paid** lessons go to `protected/`; the auth server only serves them to a
  paying member. No banner.
- Both appear in `public/lessons.html`, which has the header's live search
  filter enabled. Paid entries link to `/protected/NNN.html`.

## Editing or removing a lesson

- **Edit**: change the `.md` file, re-run `./parser.sh`.
- **Remove**: delete BOTH the source and the built file, then rebuild so the
  index drops it:

  ```bash
  rm content/free/010.md public/010.html
  ./parser.sh
  ```

## Gotchas (lessons)

- Always re-run `./parser.sh` after any `.md` change — editing the `.html`
  directly will be overwritten on the next build.
- Numbers must be zero-padded to 3 digits (`007.md`, not `7.md`); the related-
  lesson resolver pads refs to match filenames.
- A related-lesson `warn: lesson 'X' not found` means the referenced number
  doesn't exist in that collection — fix the ref or create the lesson.
- `parser.sh` requires `pandoc` to be installed.


