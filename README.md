# NAVYLILY.TV

Navy Lily is a small self-hosted platform for selling and serving online drawing
lessons in Portuguese, live at tiagohierath.com. The whole thing is one Go server
plus a shell build step: pandoc turns lesson markdown into static HTML, and the
Go binary serves those files, handles login, gates the paid course, and processes
Brazilian payments (PIX and card, via AbacatePay) with Supabase as auth and
database.

This repository is open-core: the platform is MIT-licensed and public, but the
paid lesson content is not included. It lives outside the repo and is dropped
into content/paid/ and protected/ (both git-ignored) at deploy time. SAMPLE
placeholder files show the expected shape, so the app runs out of the box with
your own lessons.

This README is the full documentation. It is ordered as a tour: the content
build first, then the server, then payments, then everything around them.
Reading it top to bottom is enough to understand, and edit, every part of the
system in one sitting.

## The big picture

```
content/free/NNN.md  --+                        +--> public/NNN.html      served at /
                       +--  parser.sh (pandoc) -+
content/paid/NNN.md  --+                        +--> protected/NNN.html   served at /protected/  (members only)
content/WIKI/*.md    ------ parser.sh --------------> public/wiki/*.html  served at /wiki/

auth/main.go  =  the one server binary:
  serves public/ and protected/, login and sessions (Supabase),
  checkout and webhook (AbacatePay), community forum, profiles
```

There are two halves, and they barely touch:

1. A build step. parser.sh reads markdown from content/ and writes finished HTML
   into public/ and protected/. It runs offline, needs only pandoc, and is rerun
   whenever content or the template changes. pdf.sh does the same for the
   downloadable course PDF.
2. A server. auth/main.go is a single Go file that serves those directories and
   adds everything dynamic: accounts, the member gate on /protected/, payments,
   the community forum, profiles and lesson-completion tracking.

The free course ("Desenho do zero") is fully open. An active membership (Navy,
R$497 a year) unlocks the paid course ("Art Sovereignty") at /protected/ and
posting in the community.

## Repository layout

- content/ is the source markdown you edit.
- public/ is the built free site plus hand-written JS and assets. Served at /.
- protected/ is the built paid course. Served at /protected/, members only.
- auth/ is the Go server: main.go, its HTML pages in auth/web/, deploy units in
  auth/deploy/.
- parser.sh and template.html build the site. pdf.sh builds the PDF.
- supabase/migrations/ is the database schema.
- navyfetch.sh prints a stats banner in the terminal (accounts, visits, uptime).
- wiki2build.sh is a legacy one-shot generator for content/wiki2; the live wiki
  build is the one inside parser.sh.

## Part 1: from markdown to HTML

Run ./parser.sh to rebuild every page. It needs pandoc, reads content/, writes
public/ and protected/, and prints one line per file built. There is no watch
mode: edit, rerun, refresh.

### Lesson conventions

Everything about a lesson is derived from the file itself:

- The filename is the lesson order: content/free/001.md, 002.md, and so on.
- The first "# " heading is the title.
- An optional thumbnail lives at content/free/IMAGES/NNN.jpg (also jpeg, png,
  webp). It is copied into the served tree, shown 16:9 under the lesson heading,
  and used on the lesson lists.
- Trailing lines that are just a lesson number (a line containing only "002")
  are hand-picked related lessons. They become the "Aulas relacionadas"
  collapsible at the end of the page. With none given, the numeric neighbours
  are used instead.
- Mentioning a wiki article's title anywhere in the prose (case insensitive) is
  enough to list that article in the "Navy Wiki" collapsible at the end. The
  prose itself stays link-free.

Wiki articles in content/WIKI/ follow a different convention: the filename is
the title (there is no "# " heading), and the URL is a slugified version of it
(accents stripped, lowercased, hyphens). Obsidian-style [[wikilinks]] work:
[[Article Name]] links to that article, [[006]] links to free lesson 006, and an
unresolved link falls back to plain text so nothing ever breaks. The file
content/WIKI/.substack lists which articles appear first on the /wiki index. An
empty article still builds, as a stub, so links to it keep working.

Course covers are A4 images at content/covers/SLUG.jpg (desenho-do-zero,
art-sovereignty). They are the landing page and the course-page heroes.

### What parser.sh builds

The bottom of parser.sh is the build plan; each function above it makes one kind
of page:

- build is called twice, once per course. content/free goes to public/ with the
  community banner (-V banner=1); content/paid goes to protected/ without it.
  This is where thumbnails, prev/next nav, related lessons and wiki references
  are assembled around the markdown body.
- build_wiki makes public/wiki/slug.html for each article plus the /wiki index.
- build_index makes public/lessons.html, the searchable list of every lesson.
- build_root makes public/root.html, the landing page: just the two course
  covers, Netflix-style.
- build_course makes public/desenho-do-zero.html and public/art-sovereignty.html,
  the cover-hero pages with the numbered lesson list.
- build_search makes public/search.txt, the client-side full-text index (one
  tab-separated line per page: href, title, text).

Canonical URLs come from SITE_URL (default https://tiagohierath.com), so search
engines see one spelling per page. The server backs this with 301s (/root.html
to /, /wiki.html to /wiki).

### The template

template.html is the single pandoc template every page goes through. It holds
the entire CSS (inline, in the head) and the site header (logo, profile,
community, wiki, search, the boat icon that links to checkout). Edit it and
rerun parser.sh to change anything site-wide.

Pandoc variables toggle per-page features inside it:

- banner adds the community-join widget at the end (div#navy-join plus join.js).
  Free pages only.
- order marks a numbered lesson: it plants the <!--COMPLETE--> marker (which the
  server swaps for the per-viewer "complete lesson" control at serve time) and
  loads lesson.js.
- lessonnav, prevhref, nexthref render the previous/next buttons.
- articles and search load the matching scripts on the lessons index.

Assets referenced from the template carry ?v= version query strings
(header.js?v=11, logo.png?v=3). Bump the number whenever you change the asset,
or browsers and the service worker will keep serving the old one.

### The client-side JS

All plain vanilla JS, no build step:

- public/lesson.js: device-local lesson extras. Remembers visits (continue
  reading, study streak), estimates reading time, outlines long lessons,
  bookmarking.
- public/articles.js: on the lessons index, ticks the lessons a logged-in
  student has completed and offers "continue from where you stopped".
- public/search.js: live-filters the lesson list from the search box and also
  searches forum posts via /api/search.
- public/sw.js: the offline service worker. HTML is network-first (fresh online,
  cached copy on the train), assets are cache-first, auth and payment endpoints
  are never cached.
- auth/web/header.js: swaps the header's profile link for the user's avatar when
  logged in, painted optimistically from the nl_hint cookie to avoid flashing.
- auth/web/join.js: the inline ad and checkout widget at the end of free lessons.
  It picks one banner at random per page load from the ADS array
  (public/navy-ad-1.png through navy-ad-7.png) and runs the same PIX and card
  flows as the checkout page.

## Part 2: the server

auth/main.go is the whole backend, one file, one binary. Start it with
cd auth && ./start.sh (listens on PORT, default 8090). Every route is registered
in one block at the bottom of the file: search for mux.HandleFunc and you have
the complete map of the API.

### Configuration

Everything is environment variables, documented one by one in auth/.env.example.
The server auto-loads auth/.env on start, and auth/.env.local on top of it for
local development (.env.local wins, because it is loaded first and existing keys
are never overridden). The Config struct at the top of main.go lists every
setting with a comment. Required ones (the server refuses to start without
them): SUPABASE_URL, SUPABASE_ANON_KEY, SUPABASE_SERVICE_ROLE_KEY,
ABACATE_PAY_API_KEY, ABACATE_WEBHOOK_SECRET.

### How pages are served

handleStatic serves public/ at / with a few special cases:

- /@username serves the public profile shell (auth/web/u.html).
- / serves root.html; /root.html and /wiki.html 301 to their canonical paths.
- Lesson pages are personalized: the <!--COMPLETE--> marker is replaced with the
  viewer's own completion button, so they are served with no-store.
- Everything else is cached: HTML and search.txt for 5 minutes, PDFs for 5
  minutes (with a month-stamped download name), other assets for a day, sw.js
  never (a stale service worker would pin clients to old caching logic).

A filepath.Clean guard on every file lookup means requests cannot escape the
content directories with ../ tricks.

### Login and sessions

Supabase Auth (GoTrue) does the actual authentication: email plus password,
confirmation emails, password resets. The Go server proxies it under /auth/* and
keeps its own server-side sessions: a random token in the nl_session cookie
matched against an auth_sessions row, lasting SESSION_TTL_DAYS (default 30).
currentEmail(r) is the one helper that resolves a request to a logged-in email;
everything permission-like starts there.

Magic login links (used after anonymous purchases) are rate-limited per address
so the login form cannot be used to spam an inbox.

### The paywall

handleProtected gates /protected/. No session, redirect to /comprar. Logged in
but not an active member (or lapsed), also /comprar. Active member, serve the
file from PROTECTED_DIR. Membership is one query: isActiveMember checks the
active_members view, which is just members with status active and expires_at in
the future.

### Community, profiles, progress

The rest of the routes are the member features:

- Community forum at /community: public reading, member posting, comments,
  likes, one image per post. OWNER_EMAIL (if set) can delete anything; members
  can always delete their own.
- Profiles: username, bio, avatar (one small JPEG per account, filename
  sha256(email)), public page at /@username.
- Lesson completions: /api/complete records them, /api/heatmap draws the
  GitHub-style activity heatmap on the profile.

## Part 3: payments

Payments are AbacatePay (PIX and card). The site never touches card data. An
active membership is one year of access keyed by email, and email is the single
thread through the whole flow: charges are bound to an email at creation, and
the webhook grants access to that email.

The checkout page is auth/web/comprar.html, served at /comprar (alias /navy). It
works logged in or anonymous: an anonymous buyer just types the email that will
receive access. buyerEmail(r) resolves this everywhere: the session's email if
logged in, otherwise the typed one.

### Flow 1: PIX, inline

The page POSTs /pix/new. The server creates the charge at AbacatePay, binds the
charge id to the buyer's email in memory and durably in payment_events (the PIX
webhook carries no email, so this binding is how a paid charge finds its buyer,
even across a server restart), and returns the QR code plus copy-paste code. The
page then polls /pix/status every few seconds. When AbacatePay reports PAID, the
server grants access immediately, without waiting for the webhook, and for an
anonymous buyer emails a magic login link (once per charge, rate-limited per
email). Access always goes to the email the charge was bound to at creation,
never the caller's session, so nobody can claim someone else's charge by
polling its id.

### Flow 2: card, hosted

/card/new redirects the browser to AbacatePay's hosted card checkout, billing
the pre-created product in ABACATE_PRODUCT_ID (empty disables the card button;
CARD_MAX_INSTALLMENTS caps the installments). Access is granted later by the
webhook.

### Flow 3: coupons, hosted

/coupon/check validates a code for instant feedback on the page. /checkout/new
then opens a hosted checkout (PIX and card together) against
ABACATE_ONETIME_PRODUCT_ID, with the buyer's email as externalId and the coupon
applied by AbacatePay itself. The webhook grants access by that externalId.

### The webhook

handleAbacateWebhook at /webhooks/abacatepay is the authoritative confirmation
and the only source of refunds and cancels. In order, it:

1. Verifies the ?webhookSecret= query parameter with a constant-time compare.
2. If the X-Webhook-Timestamp and X-Webhook-Signature headers are present,
   rejects replays older than 5 minutes and bodies whose HMAC-SHA256 does not
   match. Headers are only checked when present.
3. Parses a superset of AbacatePay's payload shapes, because they have drifted
   across their docs, CLI and SDK: the charge may sit under data.transparent,
   data.checkout, data.pixQrCode or data.billing, and the email may come from
   data.customer.email, the charge's externalId, our metadata, or the stored
   charge binding. The first source that has it wins.
4. Decides what to do from the event name and the status together, so a refund
   or dispute is never mistaken for a fresh payment.
5. Is idempotent: AbacatePay retries webhooks, and a (charge, event, status)
   triple that was already handled is acknowledged and ignored.

Every event, including charge-less payout events, is logged to payment_events.

### Granting access

grantAccess upserts the member row: one year, keyed by lowercased email,
extending from whichever is later, the current expiry or now, so renewing early
stacks time instead of discarding it. The original purchase date is preserved on
renewal.

auth/gift.sh grants memberships manually from the command line (used to migrate
legacy members), optionally sending a welcome email through Resend. Note that
Resend's API sits behind Cloudflare and rejects requests without a browser-like
User-Agent.

## Part 4: analytics

Analytics is GoatCounter. There is no SDK and no script tag on most pages, just
an image-pixel GET fired at the moments that matter:

```
new Image().src = 'https://stats.navylily.tv/count?p=EVENT-NAME';
```

Four funnel events exist, and grepping for stats.navylily.tv finds every call
site:

- cta-home fires in public/root.html when a course cover is clicked.
- cta-pricing fires on the buy buttons, in auth/web/comprar.html and
  auth/web/join.js.
- checkout-start fires when a charge is opened or the buyer leaves for the
  hosted card page.
- purchase fires when a poll sees the payment confirmed.

To add an event, fire the pixel with a new name at the moment you care about; it
shows up in GoatCounter automatically.

## Part 5: the PDF

pdf.sh builds the downloadable course book: every free lesson plus the wiki as a
final section, A4, named Navylily_<Month>_<Year>.pdf under public/downloads/.
The pipeline is markdown to typst (via pandoc) to PDF (via the typst binary), so
it needs both pandoc and typst installed.

The look of the book lives in the emit_front function: the typst #set rules for
page size, margins, the 21pt body text, the footer, and the cover page. The
cover art is picked automatically: the newest free lesson that has a thumbnail,
placed as a full-bleed square. Wikilinks are flattened to text and site-relative
links are made absolute so they work from a PDF reader.

In production a systemd timer (auth/deploy/navylily-pdf.timer) rebuilds it on
the 1st of every month; each build atomically replaces the previous one, so the
old edition disappears in the same step.

## Part 6: the database

supabase/migrations/ holds the whole Postgres schema as timestamped SQL files,
applied in order (Supabase SQL editor, or supabase db push). They define:

- members, plus the active_members view the paywall queries
- payment_events, the append-only log the webhook and charge bindings use
- auth_sessions, the server-side login sessions
- profiles, lesson_completions, and the forum tables (posts, comments, likes)
- row-level security on all of it

To change the schema, add a new timestamped migration. Never edit an old one:
they have already run in production.

## Part 7: deploy and operations

Branches:

- sophie is the live branch. All work happens here; it is what runs on the VPS.
- main mirrors sophie so pulls have a default branch to track. Sync it with
  git push origin sophie:main.
- flower-ui preserves the retired 2026 flower theme.

Deploying is intentionally dumb: push sophie, then on the server git reset to
the new commit, go build -o navylily-auth . inside auth/, and
systemctl restart navylily. Content-only changes do not even need the rebuild:
the server rereads files from disk, and parser.sh is rerun on the box to
regenerate the HTML.

The production setup is documented step by step in auth/deploy/DEPLOY.md: the
Go server as a systemd unit (navylily.service; WorkingDirectory matters, the
binary resolves web/, ../public and .env relative to it), exposed through a
Cloudflare Tunnel (cloudflared.service), so no inbound ports are ever opened on
the host. Logs are journalctl -u navylily -f.

navyfetch.sh is a bashrc banner that shows paid and free account counts, 30-day
visits and an up/down check, cached daily so shells open instantly.

## Where do I edit X

- A lesson's text or images: content/free/NNN.md (paid course: content/paid),
  then ./parser.sh.
- Site-wide layout, colors, header, CSS: template.html, then ./parser.sh.
- The landing page covers: content/covers/ and build_root in parser.sh.
- A wiki article: content/WIKI/Name.md, then ./parser.sh. Index order:
  content/WIKI/.substack.
- The checkout page: auth/web/comprar.html. The inline lesson-end widget and its
  ad banners: auth/web/join.js and public/navy-ad-*.png.
- The price: PRICE_CENTS in auth/.env. Products: ABACATE_PRODUCT_ID (card),
  ABACATE_ONETIME_PRODUCT_ID (coupon checkout).
- Payment logic and the webhook: auth/main.go, handleBuy through
  handleAbacateWebhook.
- The paywall: handleProtected in auth/main.go.
- An analytics event: grep stats.navylily.tv, add a pixel call.
- The PDF's look: emit_front in pdf.sh.
- A route: the mux.HandleFunc block at the bottom of auth/main.go.
- The schema: a new file in supabase/migrations/.

## Running it yourself

Requirements: Go 1.21+, pandoc, a Supabase project (cloud or self-hosted), and
an AbacatePay account for payments (not needed just to run locally).

```bash
# 1. Configure
cd auth && cp .env.example .env     # fill in SUPABASE_URL + keys
cd ..

# 2. Apply the schema
#    run supabase/migrations/*.sql in order in the Supabase SQL editor,
#    or `supabase db push` with the Supabase CLI

# 3. Build the pages and start the server
./parser.sh
cd auth && ./start.sh
```

Visit http://localhost:8090. The free course is at /, the wiki at /wiki, the
gated course at /protected/, checkout at /comprar.

## License

[MIT](LICENSE).
