# Navy Lily — Auth & Payments

A tiny, zero-dependency Go server that handles **everything related to login and
paid access**. The rest of the site stays static (markdown → pandoc → HTML).

It does three things:

1. **Login** — email + password via your self-hosted Supabase Auth (GoTrue),
   which hashes passwords and sends the confirmation / reset emails. On top of
   that we mint **our own opaque, revocable sessions** (pilcrowonpaper model): the
   cookie is a `<id>.<secret>` token, we store only the secret's SHA-256 hash in
   Postgres and compare it in constant time, and logout deletes the row — so a
   leaked cookie store can't be replayed and any session can be killed server-side.
2. **Payments** — creates **AbacatePay** PIX charges (Checkout Transparente) and
   grants/revokes membership from their webhooks.
3. **Gatekeeping** — only logged-in, paying members can open `protected/` lessons.

Data lives in your **self-hosted Supabase Postgres** (no SQLite). The server never
opens a DB connection directly — it talks to Supabase over HTTP (GoTrue + PostgREST),
so there are **no Go dependencies** and nothing extra to install.

```
auth/
├── main.go                 the whole server (stdlib only)
├── web/                    login / callback / PIX checkout pages
├── .env.example            copy to .env and fill in
└── start.sh                build + run
```

The database schema lives in `../supabase/migrations/`.

## Setup (one time)

### 1. Create the tables
Apply the SQL files in `../supabase/migrations/` (in filename order) with
`supabase db push`, or paste them into **Supabase Studio → SQL Editor**. This
creates `members`, `payment_events`, the `active_members` view, `auth_sessions`
(our login sessions), `profiles`, `lesson_completions` and the forum tables.
Auth users themselves are managed by Supabase Auth (`auth.users`) — you don't
create those.

### 2. Configure Supabase Auth (so login emails work)
In your self-hosted Supabase `.env`:
- Enable **email + password** signups and keep email confirmations **on**, e.g.
  `GOTRUE_EXTERNAL_EMAIL_ENABLED=true` and `GOTRUE_MAILER_AUTOCONFIRM=false`.
- Set **SMTP** settings (host, user, pass, sender) so GoTrue can send the
  confirmation and password-reset emails — point this at your **Resend** SMTP.
- Add the callback to **GOTRUE_URI_ALLOW_LIST**, e.g. `http://localhost:8090/auth/callback`
  (and your production URL later). Restart the auth container.

### 3. Configure this server
```bash
cd auth
cp .env.example .env
# fill in SUPABASE_URL, ANON_KEY, SERVICE_ROLE_KEY (from your Supabase .env),
# ABACATE_PAY_API_KEY and ABACATE_WEBHOOK_SECRET (from AbacatePay).
```

### 4. Point AbacatePay at the webhook
In the AbacatePay dashboard → **Webhooks**, add (with your secret appended):
```
https://YOUR-DOMAIN/webhooks/abacatepay?webhookSecret=YOUR_SECRET
```
Use the same value you put in `ABACATE_WEBHOOK_SECRET`. The server constant-time
compares the `webhookSecret` query param and rejects anything that doesn't match.

### 5. Run it
```bash
./start.sh        # builds and starts on PORT (default 8090)
```

## How a user experiences it

1. Creates an account at `/signup` (email + password) → confirms via the email
   GoTrue sends → lands logged in. Returning users sign in at `/login`; forgot
   their password? `/forgot` emails a reset link.
2. Opens a paid lesson under `/protected/...`. Not a member yet? They're sent to
   `/comprar`, which generates a PIX charge and shows the QR + copy-paste code.
3. They pay by PIX. The page polls `/pix/status`; AbacatePay also fires the
   `transparent.completed` webhook. Either path grants 1-year membership (keyed by
   email), and the page forwards them into the lessons.

Buying without an account still works: the **Join** widget on free lessons takes
an email + PIX/card inline, and on a confirmed payment emails a one-time login
link so the buyer can open the paid lessons (they can set a password later via
`/forgot`).

## Routes
| Route | Purpose |
|-------|---------|
| `GET /login` | Sign-in page (email + password) |
| `GET /signup` | Create-account page |
| `GET /forgot` | Request a password reset |
| `GET /reset` | Set a new password (from the reset link) |
| `POST /auth/login` | Verifies credentials, creates a session |
| `POST /auth/signup` | Registers the account (GoTrue emails confirmation) |
| `POST /auth/recover` | Sends the password-reset email |
| `POST /auth/reset` | Sets the new password, creates a session |
| `GET /auth/callback` | Completes link-based logins, sets session cookie |
| `POST /auth/logout` | Logs out (revokes the session server-side) |
| `GET /me` | JSON: `{logged_in, email, member}` — handy for your UI |
| `GET /comprar` | PIX checkout page (login required) |
| `GET /pix/new` | Creates a PIX charge, returns `{id, brCode, brCodeBase64}` |
| `GET /pix/status` | Polls a charge; grants access on PAID |
| `GET /protected/*` | Paid lessons (gated) |
| `POST /webhooks/abacatepay` | AbacatePay payment webhook |

## Notes
- Put your paid lesson HTML files in the repo's `protected/` folder
  (or change `PROTECTED_DIR`). Free lessons stay in `public/` as today.
- Membership is 1 year from purchase (the 197 BRL/year plan; set `PRICE_CENTS`);
  refunds/disputes/cancellations from AbacatePay automatically revoke access.
- Set `COOKIE_SECURE=true` once you're behind HTTPS in production.

## Gifting free access (old members)

To give legacy members a free year — no charge, no card, only e-mail login —
use `gift.sh`. It writes `members` rows (`source=gift`, 1-year expiry) straight
to Supabase with the service-role key, so there's no public admin endpoint.

```bash
cd auth
./gift.sh --dry-run members.txt   # preview — writes nothing
./gift.sh members.txt             # grant the gift
./gift.sh --notify members.txt    # grant + e-mail each NEW member (Resend)
./gift.sh ana@x.com bia@y.com     # or pass e-mails directly
```

`members.txt` is one entry per line (blank lines and `#comments` ignored); an
optional name can follow a comma: `ana@example.com, Ana Souza`. Re-running is
safe — anyone who already paid or was already gifted is left untouched. The
member opens the e-mailed link, creates an account with that same e-mail
(email + password), and the gate lets them into the paid lessons.

`--notify` needs `RESEND_API_KEY`, `RESEND_FROM` and a public `SITE_URL` in
`.env` (it refuses to send a `localhost` login link). See `./gift.sh --help`.
