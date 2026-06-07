#!/usr/bin/env bash
# Navy Lily — funnel stats for the terminal (call from ~/.bashrc).
# Users+Paid: Supabase RPC public.funnel_stats(days). Visitors: Cloudflare GraphQL.
# All three numbers cover roughly the same NAVYLILY_STATS_DAYS window (default 30):
# Supabase uses a rolling instant window, Cloudflare whole UTC days (today partial),
# so the two conversion percentages are close but not exact. Cached + short timeouts
# so opening a shell never blocks; Cloudflare/Supabase run at most once per `ttl`.
set -u
cd "$(dirname "$0")" || exit 0

DAYS="${NAVYLILY_STATS_DAYS:-30}"
cache="${TMPDIR:-/tmp}/navylily-stats.${DAYS}.txt"
ttl="${NAVYLILY_STATS_TTL:-86400}"   # refresh at most once a day (well under any API limit)

# Load only the keys we need from auth/.env (no blind sourcing of the whole file).
if [ -f .env ]; then
  set -a
  . <(grep -E '^(SUPABASE_URL|SUPABASE_SERVICE_ROLE_KEY|CF_API_TOKEN|CF_ZONE_TAG)=' .env)
  set +a
fi

render() {
  local sb users paid visitors since until q body v2s s2p
  # Supabase: signups + active paid members in the window.
  sb=$(curl -s --max-time 4 \
        -H "apikey: ${SUPABASE_SERVICE_ROLE_KEY:-}" \
        -H "Authorization: Bearer ${SUPABASE_SERVICE_ROLE_KEY:-}" \
        -H 'Content-Type: application/json' \
        -d "{\"days\":$DAYS}" \
        "${SUPABASE_URL:-}/rest/v1/rpc/funnel_stats")
  users=$(printf '%s' "$sb" | jq -r '.[0].users // empty' 2>/dev/null)
  paid=$( printf '%s' "$sb" | jq -r '.[0].paid  // empty' 2>/dev/null)
  [ -z "$users" ] && return 1            # Supabase unreachable -> keep stale cache

  # Cloudflare: sum of daily unique visitors over the window (optional).
  # Note: summing daily uniques slightly over-counts repeat visitors across days.
  visitors=""
  if [ -n "${CF_API_TOKEN:-}" ] && [ -n "${CF_ZONE_TAG:-}" ]; then
    since=$(date -u -d "$((DAYS-1)) days ago" +%F); until=$(date -u +%F)
    q='{ viewer { zones(filter:{zoneTag:"'"$CF_ZONE_TAG"'"}) { httpRequests1dGroups(limit:'"$DAYS"', filter:{date_geq:"'"$since"'", date_leq:"'"$until"'"}) { uniq { uniques } } } } }'
    body=$(curl -s --max-time 4 https://api.cloudflare.com/client/v4/graphql \
            -H "Authorization: Bearer $CF_API_TOKEN" -H 'Content-Type: application/json' \
            --data "$(jq -n --arg q "$q" '{query:$q}')")
    visitors=$(printf '%s' "$body" | jq -r '[.data.viewer.zones[0].httpRequests1dGroups[].uniq.uniques] | add // empty' 2>/dev/null)
  fi

  v2s=$(awk -v a="$users" -v b="${visitors:-0}" 'BEGIN{if(b>0)printf "%.1f%%",a/b*100; else printf "n/a"}')
  s2p=$(awk -v a="$paid"  -v b="$users"          'BEGIN{if(b>0)printf "%.1f%%",a/b*100; else printf "n/a"}')

  printf 'Navy Lily — last %s days\n' "$DAYS"
  printf 'Visitors: %s\n' "${visitors:-n/a}"
  printf 'Users: %s\n'    "$users"
  printf 'Paid: %s\n\n'   "$paid"
  printf 'Visit → Signup: %s\n' "$v2s"
  printf 'Signup → Paid: %s\n'  "$s2p"
}

fresh() { [ -f "$cache" ] && [ $(( $(date +%s) - $(stat -c %Y "$cache") )) -lt "$ttl" ]; }

if fresh; then
  cat "$cache"
elif [ -f "$cache" ]; then
  cat "$cache"                                  # show stale instantly...
  ( out=$(render) && printf '%s\n' "$out" >"$cache" ) >/dev/null 2>&1 & disown
else
  out=$(render) && { printf '%s\n' "$out"; printf '%s\n' "$out" >"$cache"; }
fi
