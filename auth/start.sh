#!/bin/bash
# Build and run the Navy Lily auth + payments server.
# Usage: ./start.sh
set -euo pipefail
cd "$(dirname "$0")"

if [ ! -f .env ]; then
  echo "No auth/.env found. Copy .env.example to .env and fill it in:"
  echo "  cp .env.example .env"
  exit 1
fi

go build -o navylily-auth .
exec ./navylily-auth
