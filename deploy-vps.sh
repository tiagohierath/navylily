#!/usr/bin/env bash
# Run on the production VPS. One command updates code/content, verifies the
# build, restarts atomically, checks health, and invalidates Cloudflare.
set -euo pipefail

REPO_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
BRANCH=${DEPLOY_BRANCH:-sophie}
cd "$REPO_DIR"

if [[ -n "$(git status --porcelain --untracked-files=no)" ]]; then
  echo "Tracked VPS files have local changes; refusing to overwrite them:" >&2
  git status --short --untracked-files=no >&2
  exit 1
fi

git fetch origin "$BRANCH"
git merge --ff-only "origin/$BRANCH"

# Rebuild generated HTML on the VPS as well, including git-ignored paid content.
./parser.sh

(
  cd auth
  CGO_ENABLED=0 go test ./...
  CGO_ENABLED=0 go build -o navylily-auth.new .
  mv navylily-auth.new navylily-auth
)

sudo systemctl restart navylily
curl --fail --silent --show-error --max-time 10 \
  http://127.0.0.1:8090/me >/dev/null

./auth/purge-cloudflare.sh
echo "Navy Lily deployed from $BRANCH and healthy."
