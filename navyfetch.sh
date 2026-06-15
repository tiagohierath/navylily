#!/usr/bin/env bash
# navyfetch — fastfetch-style navylily stats banner for .bashrc.
#
# Prints paid/free account counts, 30-day visits and an UP/DOWN check.
# All numbers come from APIs (Supabase GoTrue + PostgREST, Cloudflare
# GraphQL) and are cached for a day so opening a terminal stays instant;
# only the first shell of the day pays for the calls.
#
#   navyfetch.sh        print (refreshes at most once a day)
#   navyfetch.sh -f     force a refresh now
#
# Credentials are read from auth/.env next to this script. Visits need
# CF_API_TOKEN (a Cloudflare token with Zone > Analytics:Read for
# tiagohierath.com); until it's filled in, visits show as "?".

set -u

SITE="https://tiagohierath.com"
ZONE_NAME="tiagohierath.com"
ENV_FILE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/auth/.env"
CACHE="${XDG_CACHE_HOME:-$HOME/.cache}/navyfetch"
TODAY=$(date +%F)

# Serve from cache unless it's from a previous day or -f was given.
if [ "${1:-}" != "-f" ] && [ -f "$CACHE" ] && [ "$(head -n1 "$CACHE")" = "$TODAY" ]; then
    tail -n +2 "$CACHE"
    exit 0
fi

# .env is parsed by the Go server, not bash (values may contain < >),
# so pull single keys out with grep instead of sourcing it.
envget() { grep -oP "^$1=\K.*" "$ENV_FILE" 2>/dev/null | head -n1; }

SUPABASE_URL=$(envget SUPABASE_URL)
SRK=$(envget SUPABASE_SERVICE_ROLE_KEY)
CF_API_TOKEN=$(envget CF_API_TOKEN)
CF_ZONE_TAG=$(envget CF_ZONE_TAG)

CURL=(curl -s --max-time 6)
SB=(-H "apikey: $SRK" -H "Authorization: Bearer $SRK")

# --- up check ----------------------------------------------------------
code=$("${CURL[@]}" -o /dev/null -w '%{http_code}' "$SITE/" || echo 000)
if [ "$code" -ge 200 ] && [ "$code" -lt 400 ]; then
    status=$'\033[1;32m● UP\033[0m'
else
    status=$'\033[1;31m● DOWN ('"$code"$')\033[0m'
fi

# --- paid / free accounts ----------------------------------------------
# Accounts come from GoTrue's admin list; "paid" means the account's
# e-mail also appears in the active_members view. Comparing locally
# keeps gift members without a login out of the count.
accounts=""
page=1
while [ "$page" -le 50 ]; do
    chunk=$("${CURL[@]}" "${SB[@]}" \
        "$SUPABASE_URL/auth/v1/admin/users?page=$page&per_page=100" \
        | jq -r '.users[]?.email // empty' 2>/dev/null) || break
    [ -z "$chunk" ] && break
    accounts+="$chunk"$'\n'
    page=$((page + 1))
done

members=$("${CURL[@]}" "${SB[@]}" -H "Range: 0-99999" \
    "$SUPABASE_URL/rest/v1/active_members?select=email" \
    | jq -r '.[]?.email // empty' 2>/dev/null)

if [ -n "$accounts" ]; then
    total=$(grep -c . <<<"$accounts")
    paid=$(comm -12 \
        <(tr 'A-Z' 'a-z' <<<"$accounts" | grep . | sort -u) \
        <(tr 'A-Z' 'a-z' <<<"$members" | grep . | sort -u) | wc -l)
    free=$((total - paid))
else
    paid="?" free="?"
fi

# --- visits, last 30 days (Cloudflare GraphQL) --------------------------
visits="?"
hint='set CF_API_TOKEN in auth/.env for visits'
if [ -n "$CF_API_TOKEN" ]; then
    hint='CF_API_TOKEN is invalid or lacks Analytics:Read'
    # Zone tag is discovered from the token once per refresh if not set.
    if [ -z "$CF_ZONE_TAG" ]; then
        CF_ZONE_TAG=$("${CURL[@]}" -H "Authorization: Bearer $CF_API_TOKEN" \
            "https://api.cloudflare.com/client/v4/zones?name=$ZONE_NAME" \
            | jq -r '.result[0].id // empty' 2>/dev/null)
    fi
    since=$(date -u -d '30 days ago' +%F)
    q="{ viewer { zones(filter: {zoneTag: \\\"$CF_ZONE_TAG\\\"}) { httpRequests1dGroups(limit: 31, filter: {date_geq: \\\"$since\\\"}) { uniq { uniques } } } } }"
    v=$("${CURL[@]}" "https://api.cloudflare.com/client/v4/graphql" \
        -H "Authorization: Bearer $CF_API_TOKEN" -H "Content-Type: application/json" \
        -d "{\"query\":\"$q\"}" \
        | jq -r '[.data.viewer.zones[0].httpRequests1dGroups[].uniq.uniques] | add // empty' 2>/dev/null)
    [ -n "$v" ] && visits=$v
fi

# --- render & cache -----------------------------------------------------
B=$'\033[1;34m' D=$'\033[2m' R=$'\033[0m'
out=$(printf '%s\n' \
    " ${B}⚜ navylily${R} ${D}· ${SITE#https://}${R}   $status" \
    "   ${B}members${R}  $paid paid · $free free" \
    "   ${B}visits${R}   $visits ${D}(last 30 days)${R}" \
    "   ${D}as of $TODAY$([ "$visits" = "?" ] && printf ' · %s' "$hint")${R}")

mkdir -p "$(dirname "$CACHE")"
printf '%s\n%b\n' "$TODAY" "$out" >"$CACHE"
printf '%b\n' "$out"
