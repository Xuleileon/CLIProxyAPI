# Zeabur Deployment Guide

## Problem (2026-07-29)

When `DEPLOY=cloud` is set but no `config.yaml` exists, CLIProxyAPI enters standby mode and does not start the HTTP server. Zeabur then returns **502 Bad Gateway** because nothing listens on the configured port.

## Required Environment Variables

| Variable | Purpose |
|----------|---------|
| `DEPLOY` | Set to `cloud` for cloud standby/bootstrap behavior |
| `CPA_PORT` | HTTP port (use `8080` to match Zeabur `web` port) |
| `CPA_API_KEY` | API key for proxy clients |
| `CPA_MANAGEMENT_KEY` | Management API secret key |
| `PORT` | Zeabur injects `${WEB_PORT}`; usually `8080` |

Optional:

| Variable | Purpose |
|----------|---------|
| `PASSWORD` | Not read automatically; use management API key instead |

## How It Works

`scripts/zeabur-entrypoint.sh` runs before the app:

1. Creates `/CLIProxyAPI/auths`
2. Generates `/CLIProxyAPI/config.yaml` from env vars when missing
3. Starts `CLIProxyAPIPlus`

After redeploy, verify:

```bash
curl -sS -o /dev/null -w "%{http_code}\n" https://<your-domain>.zeabur.app/v1/models \
  -H "Authorization: Bearer <CPA_API_KEY>"
```

Expected: `200` (or another non-502 response once auth files are configured).

## Service URL

- Domain: `https://cpaplusplus.zeabur.app`
- Zeabur console: [cli-proxy-api service](https://zeabur.com/projects/6a69717d0d0b094201bcbc92/services/6a69717dd3c296cbf58d3408?envID=6a69717d5f062718bc7b1ee7)
