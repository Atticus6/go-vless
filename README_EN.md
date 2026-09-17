# go-vless

[中文](./README.md)

[![Release](https://github.com/Atticus6/go-vless/actions/workflows/release.yml/badge.svg)](https://github.com/Atticus6/go-vless/releases)

VLESS over WebSocket proxy: multi-user, traffic stats, Argo tunnel, and a web admin page — one config for VPS / Docker / Vercel / Railway.

## Features

- Multi-user: comma-separated UUIDs, per-user traffic accounting
- Argo tunnel: works without a public IP; direct VPS connection works too
- Admin page: tunnel status, access URLs, egress IPs, traffic, memory — all in one place
- User management: add/remove users online, no restart needed
- One-click install: VPS script, Docker, and Vercel supported

## Quick start

### Docker Compose (recommended)

```bash
UUID=<your-uuid> CONFIG_KEY=<admin-key> docker compose up -d
```

Pin a version with `IMAGE_TAG=0.1`, change the host port with `HOST_PORT=9090`, view logs with `docker logs -f go-vless`.

### One-click VPS script

```bash
curl -fsSL https://raw.githubusercontent.com/Atticus6/go-vless/main/install.sh | sudo bash
```

It asks binary (default) vs Docker and whether to enable the tunnel. Afterwards:

```bash
sudo ./install.sh update    # upgrade to latest
sudo ./install.sh uninstall # uninstall
```

### Vercel

Import the repo and deploy, then add env vars in the dashboard (at least `UUID` and `CONFIG_KEY`) and redeploy.

## Configuration

Env vars and CLI flags are equivalent, flags win. Restart after changing config (`docker compose up -d`, or `systemctl restart go-vless` for the script binary install).

| Env var         | Flag            | Default | Description                                              |
| --------------- | --------------- | ------- | -------------------------------------------------------- |
| `UUID`          | `-uuid`         | —       | User UUID(s), **required**, comma-separated for multi-user |
| `PORT`          | `-port`         | `8080`  | Listen port (auto-overridden on Vercel/Railway)          |
| `TUNNEL`        | `-tunnel`       | `true`  | Argo tunnel toggle; defaults off on Vercel               |
| `TUNNEL_PROTO`  | `-tunnel-proto` | `auto`  | Tunnel protocol: `auto` / `quic` / `http2`, try another if it won't connect |
| `MAX_CONN`      | `-max-conn`     | `4096`  | Max concurrent connections, 503 beyond                   |
| `CONFIG_KEY`    | —               | empty   | Admin page key; **empty means the admin page returns 404** |
| `DOMAIN`        | —               | empty   | Your custom domain, shown on the admin page              |
| `VERCEL_URL` / `NF_HOSTS` / `RAILWAY_PUBLIC_DOMAIN` | — | empty | Platform domains / extra hosts, shown on the admin page |
| `SKIP_BBR`      | —               | empty   | Set `1` to skip BBR                                      |

## Admin page

Open in a browser (a missing/wrong `CONFIG_KEY` gives 404 — that's by design):

```
http://<server-ip>:<port>/config?key=<CONFIG_KEY>
```

Shows: tunnel on/off, tunnel address, access URLs, IPv4/IPv6 egress status, public egress IPs, **per-user traffic**, memory usage, uptime.

Traffic: `up` = sent by the user, `down` = received by the user; **counters reset on restart**.

### Add/remove users online

```bash
# Add (an invalid UUID rejects the whole batch)
curl -X POST 'http://<ip>:<port>/config/users/add?key=<CONFIG_KEY>' \
  -H 'Content-Type: application/json' \
  -d '{"uuids":["aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"]}'

# Remove (unknown IDs are ignored)
curl -X POST 'http://<ip>:<port>/config/users/remove?key=<CONFIG_KEY>' \
  -H 'Content-Type: application/json' \
  -d '{"uuids":["aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"]}'
```

Note: runtime changes live in memory only — after a restart the `UUID` env var wins; removing a user doesn't kick its active connections (new ones are rejected immediately).

## Connecting clients

- With tunnel: use the tunnel address (`tunnelURL` on the admin page), port `443`, `ws` transport, path `/`
- Direct VPS: use the server IP/domain, port `PORT`, path `/`

## Updating

- Docker: `docker compose pull && docker compose up -d` (pin `IMAGE_TAG` to stay on a version)
- One-click script: `sudo ./install.sh update [version, latest if omitted]`
- Vercel: redeploy in the dashboard
- Build it yourself: `go build -o server .`

## FAQ

1. **Admin page 404?** Check `CONFIG_KEY` first; unset means 404 — set it.
2. **Tunnel address stays empty?** It's still establishing right after boot — refresh and retry; if it never appears, try `TUNNEL_PROTO=quic` or `http2`.
3. **IPv6 not working?** Check the `ipv6` field on the admin page; if the server has no IPv6 egress, related requests are fast-rejected instead of hanging.
4. **Change ports in Docker?** Edit the `ports` mapping in `docker-compose.yml` (e.g. `"9090:8080"`) and `up -d` to recreate.
