#!/usr/bin/env bash
# Purge Navy Lily's Cloudflare edge cache after a successful deploy.
# Credentials stay in auth/.env (already git-ignored):
#   CF_API_TOKEN=token with Zone > Cache Purge > Purge
#   CF_ZONE_TAG=the navylily.tv zone ID
set -euo pipefail

AUTH_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
ENV_FILE=${CF_ENV_FILE:-"$AUTH_DIR/.env"}

envget() {
  local key=$1
  awk -v key="$key" 'index($0, key "=") == 1 {
    sub("^" key "=", "")
    sub(/[[:space:]]+#.*$/, "")
    gsub(/^['"'"']|['"'"']$/, "")
    print
    exit
  }' "$ENV_FILE" 2>/dev/null
}

api_token=${CF_API_TOKEN:-$(envget CF_API_TOKEN)}
zone_id=${CF_ZONE_TAG:-${CF_ZONE_ID:-$(envget CF_ZONE_TAG)}}

if [[ -z "$api_token" || -z "$zone_id" ]]; then
  echo "Cloudflare purge credentials missing in $ENV_FILE" >&2
  echo "Set CF_API_TOKEN and CF_ZONE_TAG before deploying." >&2
  exit 1
fi

response=$(curl --fail-with-body --silent --show-error --max-time 20 \
  -X POST "https://api.cloudflare.com/client/v4/zones/$zone_id/purge_cache" \
  -H "Authorization: Bearer $api_token" \
  -H "Content-Type: application/json" \
  --data '{"purge_everything":true}')

if [[ "$response" != *'"success":true'* ]]; then
  echo "Cloudflare rejected the cache purge: $response" >&2
  exit 1
fi

echo "Cloudflare edge cache purged."
