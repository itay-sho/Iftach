#!/bin/sh
set -e

CONFIG_DIR=/etc/cloudflared
CONFIG_FILE=$CONFIG_DIR/config.yml
CREDS_FILE=$CONFIG_DIR/credentials.json

if [ -z "$CLOUDFLARE_TUNNEL_ID" ] || [ -z "$CLOUDFLARE_TUNNEL_CREDENTIALS" ] && [ -z "$CLOUDFLARE_TUNNEL_CREDENTIALS_B64" ]; then
  echo "Error: set CLOUDFLARE_TUNNEL_ID and either CLOUDFLARE_TUNNEL_CREDENTIALS or CLOUDFLARE_TUNNEL_CREDENTIALS_B64" >&2
  exit 1
fi

mkdir -p "$CONFIG_DIR"

if [ -n "$CLOUDFLARE_TUNNEL_CREDENTIALS_B64" ]; then
  echo "$CLOUDFLARE_TUNNEL_CREDENTIALS_B64" | tr -d '\n' | base64 -d > "$CREDS_FILE"
else
  printf '%s' "$CLOUDFLARE_TUNNEL_CREDENTIALS" > "$CREDS_FILE"
fi

# Ingress: hostname from env or default iftach.rizz.co.il; service URL from env or default http://iftach:8080
HOSTNAME="${CLOUDFLARE_TUNNEL_HOSTNAME:-iftach.rizz.co.il}"
SERVICE_URL="${CLOUDFLARE_TUNNEL_SERVICE_URL:-http://iftach:8080}"

cat > "$CONFIG_FILE" << EOF
tunnel: $CLOUDFLARE_TUNNEL_ID
credentials-file: $CREDS_FILE

ingress:
  - hostname: $HOSTNAME
    service: $SERVICE_URL
  - service: http_status:404
EOF

exec cloudflared --no-autoupdate --config "$CONFIG_FILE" tunnel run "$CLOUDFLARE_TUNNEL_ID"
