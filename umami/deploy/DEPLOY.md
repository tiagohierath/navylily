# Umami — self-hosted analytics for navylily.tv

Run Umami natively on the Ubuntu VPS (no Docker). Exposed via Cloudflare Tunnel
at `umami.navylily.tv`.

## Prerequisites on the VPS

```bash
# Node.js 18+ (Ubuntu 22.04+)
curl -fsSL https://deb.nodesource.com/setup_20.x | sudo -E bash -
sudo apt install -y nodejs

# PostgreSQL
sudo apt install -y postgresql postgresql-contrib

# Build tools for native modules
sudo apt install -y build-essential
```

## Phase 1 — Database setup

```bash
sudo -u postgres psql <<EOF
CREATE USER umami WITH PASSWORD 'CHANGE_ME_STRONG_PASSWORD';
CREATE DATABASE umami OWNER umami;
GRANT ALL PRIVILEGES ON DATABASE umami TO umami;
EOF
```

## Phase 2 — Install Umami

```bash
# Clone to /opt
cd /opt
sudo git clone https://github.com/umami-software/umami.git
sudo chown -R $USER:$USER umami
cd umami

# Install dependencies and build
npm install
npm run build
```

## Phase 3 — Configure Umami

Create `/opt/umami/.env`:

```bash
DATABASE_URL=postgresql://umami:CHANGE_ME_STRONG_PASSWORD@localhost:5432/umami
```

Run the database migrations:

```bash
cd /opt/umami
npx prisma migrate deploy
```

## Phase 4 — Systemd service

```bash
sudo cp /path/to/navylily/umami/deploy/umami.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now umami
```

Check it's running:

```bash
curl -s localhost:3000  # Should return HTML
journalctl -u umami -f
```

## Phase 5 — Cloudflare Tunnel

Add to `~/.cloudflared/config.yml` under `ingress:`:

```yaml
  - hostname: umami.navylily.tv
    service: http://localhost:3000
```

Then:

```bash
cloudflared tunnel route dns navylily umami.navylily.tv
sudo systemctl restart cloudflared
```

## Phase 6 — First login and setup

1. Go to `https://umami.navylily.tv`
2. Default login: `admin` / `umami`
3. **Change the password immediately** (Settings -> Profile)
4. Add website:
   - Name: `Navy Lily`
   - Domain: `navylily.tv`
   - Copy the tracking code / website ID

## Phase 7 — Add tracking to navylily.tv

The tracking script is already in `template.html`. Just update the
`data-website-id` with your actual website ID from Umami.

## Day-to-day ops

```bash
# Logs
journalctl -u umami -f

# Restart
sudo systemctl restart umami

# Update Umami
cd /opt/umami
git pull
npm install
npm run build
npx prisma migrate deploy
sudo systemctl restart umami
```

## Backup

The data lives in PostgreSQL. Back up with:

```bash
sudo -u postgres pg_dump umami > umami_backup_$(date +%Y%m%d).sql
```
