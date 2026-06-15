#!/usr/bin/env bash
# auth/gift.sh — grant legacy / old members a free 1-year membership.
#
# A "gift" is just a row in public.members with status=active and
# expires_at = now + 1 year (both DB defaults), keyed by the member's e-mail
# and tagged source=gift. No charge, no credit card. The member creates an
# account with that same e-mail (email + password), and the gate
# (isActiveMember) sees the active row and opens the paid lessons.
#
# It writes straight to Supabase over PostgREST with the service-role key (the
# same door the Go server uses), so there is no public admin endpoint to
# secure — you run this from a trusted machine that has auth/.env.
#
# Usage:
#   ./gift.sh members.txt                  # gift everyone listed in the file
#   ./gift.sh ana@x.com bia@y.com          # gift e-mails passed directly
#   ./gift.sh --dry-run members.txt        # show what WOULD happen, write nothing
#   ./gift.sh --notify members.txt         # gift + e-mail each NEW member (Resend)
#
# File format: one per line, blank lines and #comments ignored. Optional name
# after a comma:
#   ana@example.com
#   bia@example.com, Bia Souza
#
# Safe to re-run: members who already exist (paid OR already gifted) are left
# untouched (PostgREST resolution=ignore-duplicates). --notify only mails the
# rows that were newly created on this run.
set -euo pipefail
cd "$(dirname "$0")"

DRY_RUN=0
NOTIFY=0
INPUTS=()

# ---- args ----------------------------------------------------------------
while [ $# -gt 0 ]; do
  case "$1" in
    --dry-run) DRY_RUN=1 ;;
    --notify)  NOTIFY=1 ;;
    -h|--help)
      grep '^#' "$0" | sed 's/^# \{0,1\}//' | sed '1d'
      exit 0 ;;
    --) shift; while [ $# -gt 0 ]; do INPUTS+=("$1"); shift; done; break ;;
    -*) echo "unknown option: $1" >&2; exit 2 ;;
    *)  INPUTS+=("$1") ;;
  esac
  shift
done

if [ ${#INPUTS[@]} -eq 0 ]; then
  echo "nothing to do — pass a file or some e-mails (see --help)" >&2
  exit 2
fi

# ---- tiny .env reader (mirrors the Go loader: strips inline #comments) ----
read_env() {
  local key="$1" file="${2:-.env}" line val
  [ -f "$file" ] || return 1
  line="$(grep -E "^${key}=" "$file" | head -n1)" || true
  [ -n "$line" ] || return 1
  val="${line#*=}"
  case "$val" in
    \"*|\'*) : ;;                       # quoted: keep as-is (a # may be inside)
    *) val="${val%% #*}" ;;             # strip an inline " #..." comment
  esac
  val="${val#"${val%%[![:space:]]*}"}"  # ltrim
  val="${val%"${val##*[![:space:]]}"}"  # rtrim
  case "$val" in
    \"*\") val="${val#\"}"; val="${val%\"}" ;;
    \'*\') val="${val#\'}"; val="${val%\'}" ;;
  esac
  printf '%s' "$val"
}

trim() { local s="$1"; s="${s#"${s%%[![:space:]]*}"}"; s="${s%"${s##*[![:space:]]}"}"; printf '%s' "$s"; }

SUPABASE_URL="$(read_env SUPABASE_URL || true)"
SERVICE_KEY="$(read_env SUPABASE_SERVICE_ROLE_KEY || true)"
SUPABASE_URL="${SUPABASE_URL%/}"
if [ -z "$SUPABASE_URL" ] || [ -z "$SERVICE_KEY" ]; then
  echo "missing SUPABASE_URL or SUPABASE_SERVICE_ROLE_KEY in auth/.env" >&2
  exit 1
fi

# ---- collect + validate e-mails -----------------------------------------
ENTRIES=()   # each: "email<TAB>name"
add_entry() {
  local raw="$1" email name
  raw="${raw%%#*}"                       # strip trailing comment
  raw="$(trim "$raw")"
  [ -n "$raw" ] || return 0
  if [[ "$raw" == *","* ]]; then
    email="$(trim "${raw%%,*}")"
    name="$(trim "${raw#*,}")"
  else
    email="$(trim "$raw")"; name=""
  fi
  email="${email,,}"                     # lowercase
  if [[ "$email" != *@*.* ]]; then
    echo "  skip (not an e-mail): $raw" >&2
    return 0
  fi
  ENTRIES+=("$email"$'\t'"$name")
}

for item in "${INPUTS[@]}"; do
  if [ -f "$item" ]; then
    while IFS= read -r line || [ -n "$line" ]; do add_entry "$line"; done < "$item"
  else
    add_entry "$item"
  fi
done

if [ ${#ENTRIES[@]} -eq 0 ]; then
  echo "no valid e-mails found" >&2
  exit 1
fi

# De-dupe within this batch (keep first occurrence).
mapfile -t ENTRIES < <(printf '%s\n' "${ENTRIES[@]}" | awk -F'\t' '!seen[$1]++')
TOTAL=${#ENTRIES[@]}

# ---- build the PostgREST payload ----------------------------------------
PAYLOAD="$(printf '%s\n' "${ENTRIES[@]}" | jq -R -n '
  [ inputs
    | split("\t")
    | { email:  (.[0] | ascii_downcase),
        name:   (if ((.[1] // "") | length) > 0 then .[1] else null end),
        source: "gift" } ]')"

echo "Gifting 1 free year to $TOTAL member(s)."

if [ "$DRY_RUN" -eq 1 ]; then
  echo "── dry run: nothing will be written ──"
  echo "$PAYLOAD" | jq -r '.[] | "  + \(.email)\(if .name then " (\(.name))" else "" end)"'
  echo "POST → $SUPABASE_URL/rest/v1/members  (resolution=ignore-duplicates)"
  [ "$NOTIFY" -eq 1 ] && echo "would e-mail each NEW member via Resend"
  exit 0
fi

# ---- write to Supabase ---------------------------------------------------
# ignore-duplicates => existing rows (paid or already gifted) are left alone;
# return=representation => the response holds exactly the rows we created.
RESP="$(curl -sS -w $'\n%{http_code}' -X POST "$SUPABASE_URL/rest/v1/members" \
  -H "apikey: $SERVICE_KEY" \
  -H "Authorization: Bearer $SERVICE_KEY" \
  -H "Content-Type: application/json" \
  -H "Prefer: resolution=ignore-duplicates,return=representation" \
  -d "$PAYLOAD")"
HTTP="${RESP##*$'\n'}"
BODY="${RESP%$'\n'*}"

if [ "$HTTP" -ge 300 ]; then
  echo "Supabase error ($HTTP): $BODY" >&2
  exit 1
fi

NEW_COUNT="$(jq 'length' <<<"$BODY")"
SKIPPED=$(( TOTAL - NEW_COUNT ))
echo "✓ newly gifted: $NEW_COUNT    • already had access (skipped): $SKIPPED"
jq -r '.[].email' <<<"$BODY" | sed 's/^/  + /'

# ---- optionally welcome the new members via Resend -----------------------
if [ "$NOTIFY" -eq 1 ] && [ "$NEW_COUNT" -gt 0 ]; then
  RESEND_API_KEY="$(read_env RESEND_API_KEY || true)"
  RESEND_FROM="$(read_env RESEND_FROM || true)"
  SITE_URL="$(read_env SITE_URL || true)"; SITE_URL="${SITE_URL%/}"
  if [ -z "$RESEND_API_KEY" ] || [ -z "$RESEND_FROM" ] || [ -z "$SITE_URL" ]; then
    echo "‼ --notify needs RESEND_API_KEY, RESEND_FROM and SITE_URL in auth/.env — grants saved, but no e-mails sent." >&2
    exit 1
  fi
  case "$SITE_URL" in
    *localhost*|*127.0.0.1*)
      echo "‼ SITE_URL is $SITE_URL — the login link in the e-mail would point at your machine. Set SITE_URL to your public domain before notifying." >&2
      exit 1 ;;
  esac

  echo "Sending welcome e-mails via Resend…"
  sent=0; failed=0
  while IFS= read -r to; do
    [ -n "$to" ] || continue
    to_uri="$(jq -rn --arg e "$to" '$e|@uri')"
    signup_url="$SITE_URL/signup?email=$to_uri"
    login_url="$SITE_URL/login?email=$to_uri"
    forgot_url="$SITE_URL/forgot?email=$to_uri"
    body="$(jq -n --arg from "$RESEND_FROM" --arg to "$to" --arg url "$signup_url" \
      --arg login "$login_url" --arg forgot "$forgot_url" \
      --arg subject "Seu presente: 1 ano grátis na Navy Lily 🎁" '
      { from: $from, to: [$to], subject: $subject,
        html: ("<div style=\"font-family:system-ui,sans-serif;max-width:480px;margin:auto;color:#1a1a1a\">"
          + "<h1 style=\"font-size:1.4rem\">Você ganhou 1 ano grátis 🎁</h1>"
          + "<p>Como membro antigo da Navy Lily, liberamos <strong>1 ano de acesso gratuito</strong> a todas as aulas pagas — sem cobrança e sem cartão.</p>"
          + "<p>Para entrar, crie sua senha com este e-mail:</p>"
          + "<p><a href=\"\($url)\" style=\"display:inline-block;padding:.8rem 1.2rem;background:#1f6feb;color:#fff;text-decoration:none;border-radius:8px\">Criar minha conta</a></p>"
          + "<p style=\"color:#666;font-size:.9rem\">Use este mesmo e-mail ao criar a conta — seu acesso já está liberado para ele.</p>"
          + "<p style=\"color:#666;font-size:.9rem\">Já tem conta com este e-mail? Não precisa criar outra: é só <a href=\"\($login)\">entrar</a> (ou <a href=\"\($forgot)\">redefinir sua senha</a>) — o acesso já está no mesmo e-mail.</p>"
          + "</div>") }')"
    resp="$(curl -sS -w $'\n%{http_code}' -X POST https://api.resend.com/emails \
      -H "Authorization: Bearer $RESEND_API_KEY" \
      -H "Content-Type: application/json" \
      -d "$body")"
    code="${resp##*$'\n'}"
    if [ "$code" -lt 300 ]; then sent=$((sent+1)); else
      failed=$((failed+1)); echo "  ✗ $to ($code): ${resp%$'\n'*}" >&2
    fi
  done < <(jq -r '.[].email' <<<"$BODY")
  echo "✓ e-mails sent: $sent    ✗ failed: $failed"
fi
