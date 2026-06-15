# UX DEBT in Lily

- ✅ FIXED 2026-06-12 — If you go into /navy to the checkout page, asks to login first, which is not optimal at all.
  (/comprar and /navy now serve the checkout logged out; anonymous buyers type the e-mail that
  receives access, same flow as the Join widget.)

---

Full UI/UX audit, 2026-06-12. Items grouped by area, worst first. File refs point at the evidence.

## Checkout & buying funnel

- ✅ FIXED 2026-06-12 — **Login wall in front of the money.** `handleBuy` now serves the
  checkout to everyone; comprar.html shows an e-mail field to anonymous buyers (PIX posts it,
  the card link carries it as `?email=`), and a paid anonymous charge shows the "access link
  mailed to you" confirmation instead of redirecting into the login gate. `/card/new` without
  an e-mail falls back to `/comprar`, not the login form. The 🔒 lesson/PDF redirect chain now
  ends on a working checkout.
- **Coupon system has no UI.** The server supports coupons end to end (`/coupon/check`,
  `/checkout/new?coupon=`, auth/main.go:2219–2247) but no page has a coupon field, and an
  invalid coupon redirects to `/comprar?cupom=invalido` which comprar.html never reads — the
  buyer just sees the plain checkout again with no explanation. Orphaned feature + silent failure.
- ✅ FIXED 2026-06-12 — **Expired PIX is a dead end.** Both checkouts now show a
  "Gerar novo PIX" button that opens a fresh charge in place, keeping the typed e-mail;
  comprar's failed-charge card also retries inline instead of linking a full reload.
- ✅ FIXED 2026-06-12 — **Copy-code button gives no feedback in the Join widget.** join.js now
  flips to "Copiado ✓" like comprar and falls back to `execCommand('copy')` where
  `navigator.clipboard` is missing.
- **No terms, refunds, or support anywhere near payment.** R$ 197 charged with no CDC 7-day
  withdrawal notice, no contact link, no "what happens after I pay" for the card path (which
  silently leaves the site to AbacatePay's hosted page). There is no footer site-wide, so
  there's nowhere these could even live right now.
- ✅ FIXED 2026-06-12 — **/comprar is blank until /me answers.** A 1.5s timeout now renders the
  logged-out view (community.html's pattern) and reconciles when /me lands.
- ✅ FIXED 2026-06-12 — **"liberação imediata" tag is unreadable.** Tags inside buttons are now
  light (`button .tag { color:#C9D6E8 }`), readable on the navy fill in both themes.
- **Disabled buttons look enabled.** The site-wide `button { background:#0B1F3A !important;
  color:#FFF !important }` defeats comprar's `button[disabled] { color: var(--muted) }`;
  only the lesson-complete button has a real disabled style (opacity). Pending/disabled
  states are invisible everywhere else.

## First contact: landing, sharing, signup capture

- **The landing page has no headline.** root.html's `<main>` starts at `<h2>Aulas gratuitas</h2>`
  — no h1, no one-line value proposition, no "start here". "Aprenda a desenhar de imaginação"
  exists only in the `<title>`. A first-time visitor gets a list of links with no framing.
- **No Open Graph / Twitter-card tags on any page.** Links shared on WhatsApp/Instagram — the
  main channels for a BR art audience — render with no preview image, no description. For an
  image-driven product this is the cheapest marketing fix available.
- **Every page has the same meta description** ("Aulas da Navy Lily."). The template supports
  `$description$` (template.html:7) but parser.sh never sets it, so the `$if$` never fires.
- **No free-account capture on the lessons themselves.** Logged-out readers get no
  "completar aula" button (completeWidget returns "" — auth/main.go:1147) and no nudge that a
  free account tracks progress; the only pitch for the free account lives on /signup, which
  nothing on a lesson page links to.

## Lessons index & lesson pages

- **Content jumps around after load.** On the index, three independent async inserts (local
  continue/streak/saved block, PDF link, completed-count line) each splice themselves in
  before the first `<h2>` in whatever order the fetches land (lessons.html:173–283) — layout
  shifts seconds after paint and block order varies visit to visit. Lesson pages do the same
  with read-time, TOC and bookmark row, all inserted post-paint by JS.
- **Unexplained iconography.** ▶ started, 🌊 free-done, 🪷 paid-done, 🔒 locked, grayscale
  thumbnails as "locked achievements", ⛵ placeholder — none of it is explained anywhere on
  the page. The completion metaphor is lovely and fully undiscoverable.
- **Device-local "progress" silently diverges from the account.** Bookmarks, streak, ▶ markers
  and "continuar de onde parou" live in localStorage; only completions sync. A member on a new
  phone loses all of it with no hint about which parts follow the account and which don't.
- **Keyboard shortcuts are invisible.** `/` focuses search, J/K page between lessons,
  Ctrl+Enter submits (header.js:188–212) — documented nowhere, no `?` help.

## Header, navigation, theming

- **The header is hand-copied into 10+ files and has drifted.** Auth pages (login, signup,
  forgot, comprar, profile, u) lack the Wiki button and the search box; only community.html
  marks the current page (`aria-current`); community's placeholder color differs (#3D5A80 vs
  #51719A); login.html keeps a "Perfil → /login" self-link. The duplicated ~120-line CSS block
  is the root cause — every tweak must be repeated N times and increasingly isn't.
- **Phone tap targets are too small.** At ≤480px header buttons compress to `.2rem .4rem`
  padding with `.85em` text, and the logo drops to 16px (template.html:112–119) — well under
  the ~44px touch guideline, on the most-used controls on the page.
- **check-email.html, reset.html and callback.html have no site header at all** — no logo, no
  nav, different chrome precisely at the most anxious moments (waiting for the confirmation
  e-mail, resetting a password). They also get no dark-mode toggle (header.js only appends it
  when `header.site` exists).
- **Dark-mode flash on every page.** The theme class is applied by deferred header.js, so
  dark-mode users get a white paint first, then the flip (header.js:115–183). Needs a tiny
  inline `<head>` script instead.
- **Dark mode misses inline-colored text.** The signup pitch (`style="color:#3D5A80"`,
  signup.html:48), the server-rendered error block `erroP` (#b00, auth/main.go:1458), the
  caps-lock warning, and the JS fallback errors all use inline colors that the `html.dark`
  stylesheet can't override — dark-red on near-black is ~2.4:1, and the errors are exactly
  the text a user must be able to read.
- **"Perfil" opens a login form for logged-out visitors.** Mild bait-and-switch; the button
  should read "Entrar" until there's a session (header.js swaps it to an avatar afterwards
  anyway).

## Auth flows

- **/check-email is a dead end.** No "reenviar e-mail" button (the resend form only exists
  behind a failed login with `erro=confirma`, auth/main.go:1479), no "typo'd your address?
  sign up again" path, and the same generic text serves both signup-confirmation and
  password-recovery arrivals.
- **Every login failure reads as "E-mail ou senha incorretos."** The default branch of
  handleLoginPage (auth/main.go:1481) collapses GoTrue rate-limits and outages into "wrong
  password", sending users into retype-and-lockout loops.
- **Avatar upload fails wrong.** Unlike the community composer (which shrinks client-side),
  profile.html uploads the raw file; a >8MB phone photo trips `MaxBytesReader` and the user
  is told "envie uma imagem" (auth/main.go:1939–1942) — wrong message, and the case the
  composer proves is avoidable entirely.
- **Bare, unbranded error pages.** Any 404 is Go's default English "404 page not found";
  membership-check failures render as one line of plain text ("erro ao verificar acesso").
  No layout, no nav, no way home — and these are full-page navigations, not API responses.

## Community

- ✅ RESOLVED 2026-06-12 — **Lesson threads look like a ghost wrote them.** Resolved by
  removing the feature: lessons no longer have discussion threads (forums live only in
  /community). The "Discussão desta aula" link, the /api/lesson-thread endpoint, and the 8
  empty synthetic threads in the database were all deleted.
- ✅ FIXED 2026-06-12 — **Character counters are aria-hidden.** `bindCounter` now sets
  `role=status` and only shows the counter from 80% of the cap (no "0/2000" noise);
  still the only page with `:focus-visible` outlines, rest of site rides on defaults.
- ✅ FIXED 2026-06-12 — **Compose box was a bulky form.** Twitter-style now: avatar +
  one borderless auto-growing line, 📷 icon attach (thumbnail preview with ✕ on it),
  emoji-only action row (💬 N / 🤍 N / 🔗, grayscale except the red liked heart),
  preview + tips removed. Likes added end-to-end (forum_post_likes /
  forum_comment_likes, /api/like, optimistic UI). Discord-style black splash with
  white logo on community/profile/u/comprar while data loads.
- **Heatmap data is tooltip-only.** Month counts on /@user live in `title` attributes
  (u.html:196) — invisible on touch, invisible to screen readers.

## SEO / link hygiene

- **sitemap.xml misses the whole wiki** (`Glob(PublicDir/*.html)` doesn't recurse,
  auth/main.go:1398) and lists `/root.html` and `/lessons.html` as separate URLs duplicating
  `/` and the index.
- **/@username is a soft 404**: any name returns 200 with the shell page and the generic
  "Perfil — Navy Lily" title until JS resolves; crawlers index empty profiles.
- **Content nits:** the support address in lesson 001 is plain text, not a `mailto:` link;
  the wiki entry "Perspectiva Bizantina" renders a stray "# " inside the YouTube link text
  (markdown artifact); the logo is `loading="lazy"` even though it's above the fold on
  every page.
