# Cloudflare Tunnel

The app image includes cloudflared. If tunnel env vars are set, the container runs both the app and the tunnel (one container). The tunnel exposes the app over HTTPS (TLS at Cloudflare). Configure it entirely via **environment variables** (e.g. in Portainer); no file edits required.

## Setup (all via CLI)

### 1. Install cloudflared

```bash
# macOS (Homebrew)
brew install cloudflared

# Or download from https://developers.cloudflare.com/cloudflare-one/connections/connect-apps/install-and-setup/installation/
```

### 2. Log in to Cloudflare

```bash
cloudflared tunnel login
```

This opens a browser to authorize; it saves a cert under `~/.cloudflared/`.

### 3. Create the tunnel

```bash
cloudflared tunnel create iftach
```

Note the tunnel ID from the output (or list it with `cloudflared tunnel list`).

### 4. Route DNS for the hostname

```bash
cloudflared tunnel route dns <tunnel-name-or-id> <your-hostname>
```

This creates the CNAME and enables the proxy.

### 5. Get the credentials JSON

The credentials file was written when you created the tunnel:

```bash
cat ~/.cloudflared/<TUNNEL_ID>.json
```

Copy the entire JSON (one line or pretty-printed).

## Environment variables (Portainer / docker-compose)

Set these in Portainer (or in your `.env` / compose env) so the container can run **without editing any files**:

| Variable | Required | Description |
|----------|----------|-------------|
| `CLOUDFLARE_TUNNEL_ID` | Yes | The tunnel ID from step 3. |
| `CLOUDFLARE_TUNNEL_CREDENTIALS` | One of these | The full contents of `~/.cloudflared/<TUNNEL_ID>.json`. |
| `CLOUDFLARE_TUNNEL_CREDENTIALS_B64` | One of these | Base64-encoded credentials JSON (useful if the JSON is hard to paste). |
| `CLOUDFLARE_TUNNEL_HOSTNAME` | Yes | Hostname for the tunnel (e.g. `app.example.com`). |

**Portainer:** In the stack/service, add the env vars. If tunnel vars are not set, only the app runs. For `CLOUDFLARE_TUNNEL_CREDENTIALS`, paste the JSON (multi-line is fine). Alternatively, set `CLOUDFLARE_TUNNEL_CREDENTIALS_B64` to the output of:

```bash
# Linux
base64 -w0 ~/.cloudflared/<TUNNEL_ID>.json
# macOS
base64 -i ~/.cloudflared/<TUNNEL_ID>.json
```

Then deploy the stack; the entrypoint will start the app and cloudflared in the same container.

## Config reference (generated from env in container)

When the tunnel runs, the entrypoint generates a config from env vars. Equivalent YAML:

```yaml
tunnel: <CLOUDFLARE_TUNNEL_ID>
credentials-file: /etc/cloudflared/credentials.json

ingress:
  - hostname: <CLOUDFLARE_TUNNEL_HOSTNAME>
    service: <http://127.0.0.1:${IFTACH_LISTEN_PORT}>
  - service: http_status:404
```
