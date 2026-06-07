# Navy Lily — laptop deploy (Arch + Cloudflare Tunnel)

Run the whole site (free lessons, paid lessons, auth, payments) from this one Go
binary, exposed publicly via a Cloudflare Tunnel — no router access needed, no
inbound ports, immune to a dynamic/CGNAT home IP.

Files in this folder:

- `navylily.service`        — systemd unit for the Go server
- `cloudflared.service`     — systemd unit for the tunnel (runs as user `tiago`)
- `cloudflared/config.yml`  — tunnel config TEMPLATE (copy to `~/.cloudflared/`)

---

## Phase 0 — Do first (these lag or are destructive)
- [ ] **Rotate the Resend key** in `apiresend.md` at resend.com; it's burned. Put
      the new one into `auth/.env` as `RESEND_API_KEY=`, then `rm apiresend.md`.
- [ ] **Move `tiagohierath.com` DNS to Cloudflare** — add the site in the Cloudflare
      dashboard, then set the two Cloudflare nameservers at your **registrar**
      (not the router). Propagation can take hours, so start now.

## Phase 1 — Server config
- [ ] `rm auth/.env.local`  (it forces localhost + insecure cookies and *wins*
      over `.env`).
- [ ] In `auth/.env`: `SITE_URL=https://tiagohierath.com`, `COOKIE_SECURE=true`,
      `RESEND_API_KEY=<rotated>`, `PORT=8090`; confirm Supabase + Abacate keys.
- [ ] `cd auth && go build -o navylily-auth .`

## Phase 2 — Keep the laptop alive
- [ ] `/etc/systemd/logind.conf`: `HandleLidSwitch=ignore`,
      `HandleLidSwitchExternalPower=ignore`, `HandleLidSwitchDocked=ignore`;
      then `sudo systemctl restart systemd-logind` (or reboot later).
- [ ] GNOME/KDE: disable "Automatic Suspend" in power settings (overrides logind).
- [ ] Install the server unit:
      ```
      sudo cp auth/deploy/navylily.service /etc/systemd/system/
      sudo systemctl daemon-reload && sudo systemctl enable --now navylily
      ```
- [ ] Check: `curl -s localhost:8090/me` → `{"logged_in":false,...}`

## Phase 3 — Cloudflare Tunnel (public access)
- [ ] Install cloudflared:
      ```
      sudo curl -L https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-amd64 \
        -o /usr/local/bin/cloudflared && sudo chmod +x /usr/local/bin/cloudflared
      ```
- [ ] `cloudflared tunnel login`
- [ ] `cloudflared tunnel create navylily`   (note the UUID it prints)
- [ ] `mkdir -p ~/.cloudflared && cp auth/deploy/cloudflared/config.yml ~/.cloudflared/config.yml`
      then edit it: replace `<UUID>` in both places.
- [ ] `cloudflared tunnel route dns navylily tiagohierath.com`
- [ ] Install the tunnel unit:
      ```
      sudo cp auth/deploy/cloudflared.service /etc/systemd/system/
      sudo systemctl daemon-reload && sudo systemctl enable --now cloudflared
      ```

## Phase 4 — External dashboards
- [ ] **Cloudflare → SSL/TLS → mode = Full** (not Flexible).
- [ ] **Supabase → Auth → URL Configuration:** Site URL `https://tiagohierath.com`;
      Redirect URLs add `https://tiagohierath.com/auth/callback` and `.../reset`.
- [ ] **AbacatePay → Webhooks:** `https://tiagohierath.com/webhooks/abacatepay?webhookSecret=<secret>`

## Phase 5 — Verify on the real domain
- [ ] `curl -s https://tiagohierath.com/me` works; `/` → `/001.html`
- [ ] Signup → confirm email → logged in
- [ ] Forgot → reset → logged in
- [ ] PIX buy (logged-in + anonymous Join widget) → pay → access granted; anon
      buyer gets the login-link email; check `payment_events` + `journalctl -u navylily`
- [ ] Card → hosted checkout → granted
- [ ] `gift.sh --dry-run` then real on one test email

## Phase 6 — Harden
- [ ] Full-disk encryption + screen autolock (`.env` holds the service_role + Abacate keys).
- [ ] Bind the server to `127.0.0.1:8090` so LAN/café neighbors can't reach it
      directly (only cloudflared needs it, and it's local).
- [ ] Keep it plugged in and on stable internet; don't roam with it serving.

## Day-to-day ops
- Logs: `journalctl -u navylily -f` and `journalctl -u cloudflared -f`
- Restart after a rebuild: `cd auth && go build -o navylily-auth . && sudo systemctl restart navylily`
- Tunnel health is also visible in the Cloudflare dashboard (Zero Trust → Tunnels).
