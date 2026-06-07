# Navy Lily

A small, self-hosted platform for selling and serving online lessons — a single
Go server with **Supabase** for auth and **AbacatePay** for Brazilian payments
(PIX + card). Free lessons are open to everyone; paid lessons are gated behind an
active membership.

This repository is **open-core**: the whole platform is MIT-licensed and public,
but the paid lesson *content* is not included — it lives in a private repo and is
mounted at deploy time. You can run the entire system end to end with your own
lessons.

## How it works

```
content/free/NNN.md ─┐                       ┌─► public/NNN.html     → served at /
                     ├─ parser.sh (pandoc) ──┤
content/paid/NNN.md ─┘  (private)            └─► protected/NNN.html  → served at /protected/ (members only)
```

- **`auth/`** — the Go server: email + password auth (Supabase / GoTrue), server-side
  sessions, PIX + hosted-card checkout (AbacatePay), the webhook that grants/revokes
  membership, and the gate that protects paid lessons.
- **`supabase/migrations/`** — the Postgres schema: members, payment events, sessions,
  row-level security, and the funnel-stats RPC.
- **`parser.sh` + `template.html`** — turn lesson Markdown into static HTML.
- **`public/`** — free lessons, served openly. **`protected/`** — paid lessons, gated.
  Real paid `.md`/HTML are git-ignored here; `SAMPLE` placeholders are included so the
  app runs out of the box.

## Quick start

Requirements: **Go** 1.21+, **pandoc** (builds the lessons), and a **Supabase** project
(self-hosted or cloud). Payments also need an **AbacatePay** account (not required just
to run locally).

```bash
# 1. Configure
cd auth && cp .env.example .env     # fill in SUPABASE_URL + keys, etc.
cd ..

# 2. Apply the schema
#    run the SQL files in supabase/migrations/ in your Supabase SQL editor,
#    or `supabase db push` with the Supabase CLI

# 3. Build the lessons and start the server
./parser.sh                          # content/*.md -> public/ + protected/ HTML
cd auth && ./start.sh                # builds and runs on $PORT (default 8090)
```

Visit <http://localhost:8090> — `/` serves the free lessons; `/protected/` requires an
active member.

## Configuration

All configuration is via environment variables — see
[`auth/.env.example`](auth/.env.example) for the full list (server, Supabase,
AbacatePay, Resend, content directories). No secrets are committed; `.env` is
git-ignored.

## Deployment

See [`auth/deploy/DEPLOY.md`](auth/deploy/DEPLOY.md) for a production setup using a
Cloudflare Tunnel + systemd, with no inbound ports opened on the host.

## Paid lessons

Paid lesson content is intentionally **not** in this repo. In production it is provided
from a separate private repository and dropped into `content/paid/` + `protected/`
(both git-ignored here). The `SAMPLE` files show the expected shape. All
membership/access logic lives in `auth/`.

## License

[MIT](LICENSE).
