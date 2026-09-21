# go-vless

[中文](./README.md)

[![Release](https://github.com/Atticus6/go-vless/actions/workflows/release.yml/badge.svg)](https://github.com/Atticus6/go-vless/releases)

Lightweight, fast, serverless-first VLESS proxy server (no servers to manage, pay for what you use).

> VLESS is a proxy protocol: apps on your phone/computer connect through it to browse via your server.

## Why go-vless

**Lightweight** — the serverless build is ~**6MB**, uses ~**5MB** of memory when idle, single file, download and run.

**Fast** — ready in ~**0.4s**; unreachable addresses fail instantly instead of spinning forever.

**Serverless-friendly** — stores nothing, run as many copies as you like, restarts don't matter; auto-adapts on Vercel / Railway out of the box.

## Quick start

### Serverless (Vercel, one click, no server needed)

Import the repo and deploy, add env vars in the web console (at least `UUID` and `CONFIG_KEY`), then redeploy.

### Docker (one line)

```bash
UUID=<your-uuid> CONFIG_KEY=<admin-key> docker compose up -d
```

Pin a version with `IMAGE_TAG=0.1`, change the port with `HOST_PORT=9090`, view logs with `docker logs -f go-vless`.

### VPS (one-click script)

```bash
curl -fsSL https://raw.githubusercontent.com/Atticus6/go-vless/main/install.sh | sudo bash
```

It asks direct install (default) vs Docker and whether to enable the tunnel. Note: in pipe mode, actions need `-s --` (otherwise `bash` treats the action as a file name):

```bash
curl -fsSL https://raw.githubusercontent.com/Atticus6/go-vless/main/install.sh | sudo bash -s -- update    # upgrade to latest
curl -fsSL https://raw.githubusercontent.com/Atticus6/go-vless/main/install.sh | sudo bash -s -- uninstall # uninstall
```

The installer saves itself to `/usr/local/bin/go-vless-install.sh`, so later just use it without re-downloading:

```bash
sudo go-vless-install.sh update    # upgrade to latest
sudo go-vless-install.sh uninstall # uninstall
```

## Configuration

Everything lives in env vars; restart after changing (recreate Docker, restart the service on VPS, redeploy on serverless).

| Env var         | Default | Description                                              |
| --------------- | ------- | -------------------------------------------------------- |
| `UUID`          | —       | **Required**, one key per user, separate multiple with commas |
| `PORT`          | `8080`  | Service port (auto-set on Vercel/Railway, ignore it there) |
| `TUNNEL`        | on      | Lets you connect with no public IP; auto-off on Vercel  |
| `TUNNEL_PROTO`  | `auto`  | If the tunnel won't connect, try `quic` or `http2`      |
| `MAX_CONN`      | `4096`  | Max users online at once, newcomers refused beyond that |
| `CONFIG_KEY`    | empty   | Admin page password; **empty means the admin page won't open (404)** |
| `REGISTER_URL` | empty | Dashboard address; registers only when set |
| `REGISTER_NODE_ID` | empty | Node id assigned by the dashboard |
| `DOMAIN`        | empty   | Your own domain, shown on the admin page                |
| `SSL_DOMAIN`    | empty   | Set a domain to auto-issue TLS certificates for HTTPS (full Linux + ports 80/443 required, see below) |
| `SSL_CACHE_DIR` | `./cert-cache` | Certificate cache dir (renewal relies on it; persist it before recreating containers) |
| `SSL_EMAIL`     | empty   | Contact email, optional, only for expiry notices        |
| `VERCEL_URL` / `NF_HOSTS` / `RAILWAY_PUBLIC_DOMAIN` | empty | Platform-provided addresses, shown on the admin page |
| `GO_VLESS_LANG` | empty (follows `LANG`) | Message language: `zh` Chinese / `en` English (`--lang` flag wins) |

### Automatic HTTPS certificates

On a full Linux server, point the domain at the machine, open ports 80/443, then:

```bash
SSL_DOMAIN=example.com ./go-vless
# or SSL_DOMAIN=example.com sudo ./install.sh
# Docker: uncomment the 80/443 mappings in docker-compose.yml, then SSL_DOMAIN=example.com docker compose up -d
```

The certificate is issued on demand at the first handshake on 443 (ACME HTTP-01 via port 80) and renewed automatically from disk; port 80 only serves challenges, everything else 301s to HTTPS. Once active, `/config` urls and register reports automatically include `https://domain`, and the `tls` section shows the certificate domain, expiry and days left. The plain `PORT` listener stays (tunnel origin and existing setups keep working). If 443/80 are taken, the cache dir isn't writable, or you're on serverless, it falls back to plain HTTP (check the startup logs). Slim `notunnel` builds (e.g. Vercel) exclude HTTPS entirely and always use HTTP. Flag form: `--ssl-domain example.com`.

### Language

Logs, `--help`, HTTP error messages and `install.sh` prompts are bilingual (zh/en).
English by default (when `LANG=C` or unset); a `LANG` starting with `zh` switches to Chinese:

```bash
LANG=zh_CN.UTF-8 go run . --port 8080   # Chinese logs
go run . --lang zh --port 8080          # explicit override (highest priority)
GO_VLESS_LANG=en ./install.sh           # force English installer
```

HTTP API errors (e.g. `/config/users/add`) negotiate via the request's `Accept-Language`
header; JSON field names stay in English.

### Local dev: `go run .` with dashboard registration

```bash
# split the triple (grab id/key from the node copy-install-command on the dashboard)
CONFIG_KEY=<config_key> \
REGISTER_URL=http://localhost:5173 \
REGISTER_NODE_ID=<node-id> \
go run . --port 8080
```

Or with flags (`CONFIG_KEY` still comes from env):

```bash
CONFIG_KEY=<config_key> go run . \
  --dashboard-url http://localhost:5173 \
  --node-id <node-id> \
  --port 8080
```

Registers with the dashboard at startup and keeps a heartbeat: after success it fully syncs UUIDs every 30 minutes (server-issued interval wins), retries every 60s on failure; missing any of the three means proxy-only, no registration.

## Admin page

Open in a browser (a missing/wrong password gives 404 — that's by design):

```
http://<server-ip>:<port>/config?key=<CONFIG_KEY>
```

Shows: tunnel on/off, tunnel address, usable addresses, the server's public IP, **traffic per user**, memory usage, uptime, and the **dashboard registration status** (`register` section: enabled or not, last successful sync, last error, next-sync countdown, synced user count).

Traffic: `up` = sent by the user, `down` = received by the user (plus raw `upBytes` / `downBytes` for dashboard persistence); **counters reset on restart** (same after a serverless sleep/wake cycle).

### Add/remove users online (no restart)

```bash
# Add (one bad entry rejects the whole batch)
curl -X POST 'http://<ip>:<port>/config/users/add?key=<CONFIG_KEY>' \
  -H 'Content-Type: application/json' \
  -d '{"uuids":["aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"]}'

# Remove (unknown ones are ignored)
curl -X POST 'http://<ip>:<port>/config/users/remove?key=<CONFIG_KEY>' \
  -H 'Content-Type: application/json' \
  -d '{"uuids":["aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"]}'
```

Note: runtime changes live in memory only — after a restart the `UUID` env var wins; removing a user doesn't kick its active connections (new ones are refused immediately).

## Connecting your apps

Copy the tunnel address from the `tunnelURL` line on the admin page (or use the server IP/domain without tunnel) and fill in your client app:

- Address: tunnel address, or server IP/domain
- Port: `443` with tunnel, `PORT` without
- Transport: `ws`, path: `/`
- User ID: the one from `UUID`

## Updating

- Docker: `docker compose pull && docker compose up -d` (pin `IMAGE_TAG` to stay on a version)
- One-click script: `sudo ./install.sh update [version, latest if omitted]`
- Vercel: redeploy in the web console

## FAQ

1. **Admin page won't open (404)?** Check `CONFIG_KEY` first; unset means closed — set it.
2. **Tunnel address stays empty?** It's still connecting right after boot — refresh and retry; if it never appears, try `TUNNEL_PROTO=quic` or `http2`.
3. **IPv6 not working?** Check the `ipv6` line on the admin page; if the server itself doesn't support it, it's skipped automatically instead of hanging.
4. **Change ports in Docker?** Edit the `ports` mapping in `docker-compose.yml` (e.g. `"9090:8080"`) and `up -d` to recreate.
