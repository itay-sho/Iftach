# Cloudflare Tunnel (iftach.rizz.co.il)

This tunnel exposes the iftach app over HTTPS at **iftach.rizz.co.il** (TLS at Cloudflare). Configure it entirely via **environment variables** (e.g. in Portainer); no file edits required.

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
cloudflared tunnel route dns iftach iftach.rizz.co.il
```

(Use your tunnel name or ID instead of `iftach` if different.) This creates the CNAME and enables the proxy.

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
| `CLOUDFLARE_TUNNEL_HOSTNAME` | No | Hostname for the tunnel (default: `iftach.rizz.co.il`). |
| `CLOUDFLARE_TUNNEL_SERVICE_URL` | No | Backend URL (default: `http://iftach:8080`). |

**Portainer:** In the stack/service, add the env vars. For `CLOUDFLARE_TUNNEL_CREDENTIALS`, paste the JSON (multi-line is fine). Alternatively, set `CLOUDFLARE_TUNNEL_CREDENTIALS_B64` to the output of:

```bash
# Linux
base64 -w0 ~/.cloudflared/<TUNNEL_ID>.json
# macOS
base64 -i ~/.cloudflared/<TUNNEL_ID>.json
```

Then deploy the stack; cloudflared will generate the config from these env vars and run the tunnel.
