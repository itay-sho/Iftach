#!/bin/sh
set -e

# If tunnel env is set, run both iftach (background) and cloudflared (foreground). Otherwise just iftach.
if [ -n "$CLOUDFLARE_TUNNEL_ID" ] && { [ -n "$CLOUDFLARE_TUNNEL_CREDENTIALS" ] || [ -n "$CLOUDFLARE_TUNNEL_CREDENTIALS_B64" ]; }; then
  if [ -z "$CLOUDFLARE_TUNNEL_HOSTNAME" ]; then
    echo "Error: CLOUDFLARE_TUNNEL_HOSTNAME is required when using tunnel" >&2
    exit 1
  fi

  CONFIG_DIR=/etc/cloudflared
  CONFIG_FILE=$CONFIG_DIR/config.yml
  CREDS_FILE=$CONFIG_DIR/credentials.json
  SERVICE_URL="${CLOUDFLARE_TUNNEL_SERVICE_URL:-http://127.0.0.1:8080}"

  mkdir -p "$CONFIG_DIR"
  if [ -n "$CLOUDFLARE_TUNNEL_CREDENTIALS_B64" ]; then
    echo "$CLOUDFLARE_TUNNEL_CREDENTIALS_B64" | tr -d '\n' | base64 -d > "$CREDS_FILE"
  else
    printf '%s' "$CLOUDFLARE_TUNNEL_CREDENTIALS" > "$CREDS_FILE"
  fi

  cat > "$CONFIG_FILE" << EOF
tunnel: $CLOUDFLARE_TUNNEL_ID
credentials-file: $CREDS_FILE

ingress:
  - hostname: $CLOUDFLARE_TUNNEL_HOSTNAME
    service: $SERVICE_URL
  - service: http_status:404
EOF

  /app/iftach &
  exec cloudflared --no-autoupdate --config "$CONFIG_FILE" tunnel run "$CLOUDFLARE_TUNNEL_ID"
fi

exec /app/iftach
