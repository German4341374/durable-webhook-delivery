#!/usr/bin/env sh
set -eu

if [ -f .env ]; then
  echo ".env already exists"
  exit 0
fi

umask 077
postgres_password="$(openssl rand -hex 24)"
master_key="$(openssl rand -base64 32 | tr -d '\n')"
demo_secret="$(openssl rand -hex 24)"
admin_token="$(openssl rand -hex 32)"
printf '%s\n' \
  'POSTGRES_DB=webhooks' \
  'POSTGRES_USER=webhook' \
  "POSTGRES_PASSWORD=${postgres_password}" \
  "MASTER_KEY_BASE64=${master_key}" \
  "ADMIN_TOKEN=${admin_token}" \
  "DEMO_WEBHOOK_SECRET=${demo_secret}" \
  'ALLOW_PRIVATE_TARGETS=true' \
  'WORKER_CONCURRENCY=4' \
  'LEASE_DURATION=5s' \
  'POLL_INTERVAL=200ms' > .env
echo "Created .env with local random credentials"
