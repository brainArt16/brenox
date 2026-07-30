# SDK Integration Guide

Backend reference for official Brenox SDKs (JavaScript, React, Python, Flutter). All behavior documented here is implemented in the server — no undocumented shortcuts required.

## Authentication

### Login

```bash
curl -s -X POST http://localhost:8080/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"email":"user@example.com","password":"secret123"}'
```

Response:

```json
{ "token": "eyJhbG..." }
```

Use the token as `Authorization: Bearer <token>` on REST calls and for WebSocket upgrade.

### Token refresh

Access tokens expire (default **24h**, configurable via `JWT_ACCESS_TTL_HOURS`). Refresh before expiry or within the grace window after expiry (default **7 days**, `JWT_REFRESH_GRACE_HOURS`):

```bash
curl -s -X POST http://localhost:8080/auth/refresh \
  -H 'Authorization: Bearer <current-or-recently-expired-token>'
```

Or pass the token in the body:

```bash
curl -s -X POST http://localhost:8080/auth/refresh \
  -H 'Content-Type: application/json' \
  -d '{"token":"<current-or-recently-expired-token>"}'
```

Response:

```json
{ "token": "eyJhbG..." }
```

**SDK pattern:** Refresh when API returns `401`, or proactively at ~80% of TTL. Replace stored token atomically.

## User profile

```bash
# Get profile
curl -s http://localhost:8080/api/users/me \
  -H 'Authorization: Bearer <token>'

# Update username
curl -s -X PATCH http://localhost:8080/api/users/me \
  -H 'Authorization: Bearer <token>' \
  -H 'Content-Type: application/json' \
  -d '{"username":"new_name"}'
```

## WebSocket connection

```bash
# Browser / SDK — use header or query param
wscat -c 'ws://localhost:8080/api/ws?workspace_id=1&channel_id=1&token=<jwt>'
```

See [WEBSOCKET_EVENTS.md](WEBSOCKET_EVENTS.md) for the full event catalog.

### Event envelope

Every server event includes:

| Field | Purpose |
|-------|---------|
| `type` | Event name |
| `workspace_id` | Workspace scope |
| `channel_id` | Channel scope |
| `event_id` | Unique ID for deduplication |
| `sequence` | Monotonic per-channel counter for gap detection |
| `timestamp` | UTC RFC3339Nano |
| `payload` | Event body |

Example:

```json
{
  "type": "message.new",
  "workspace_id": 1,
  "channel_id": 1,
  "event_id": "1717670400000000000-1",
  "sequence": 42,
  "timestamp": "2026-06-06T12:00:00.123456789Z",
  "payload": { "id": 1, "sender_id": 2, "content": "hello", "created_at": "..." }
}
```

## Reconnection and missed events

Recommended SDK strategy:

1. **Persist last sequence** — Store the highest `sequence` received per `(workspace_id, channel_id)`.
2. **On connect** — Subscribe via WebSocket as today.
3. **On reconnect** — After the socket opens, call REST history to backfill gaps:

```bash
curl -s 'http://localhost:8080/api/workspaces/1/channels/1/messages?limit=50&offset=0' \
  -H 'Authorization: Bearer <token>'
```

4. **Merge** — Deduplicate by message `id` / `event_id`. Apply REST messages with `created_at` newer than your last known state.
5. **Live events** — Ignore events where `sequence <= lastSequence` (duplicates from overlap).
6. **Gap detection** — If `sequence > lastSequence + 1`, fetch additional history pages until caught up.

**Heartbeat:** The server sends WebSocket ping frames every ~54s. Respond with pong (handled automatically by browser `WebSocket`).

**Backoff:** Exponential backoff on disconnect (1s → 2s → 4s … max 30s). Refresh token if reconnect fails with `401` on upgrade.

## Browser CORS

Brenox uses **two layers** of browser origin allowlisting:

1. **Platform origins** — `CORS_ALLOWED_ORIGINS` / `WS_ALLOWED_ORIGINS` for the Brenox console and first-party sites (e.g. `https://www.breno-x.com`).
2. **Per-app origins** — each developer app stores its own `allowed_origins` list. Configure via:

```bash
curl -s -X PATCH http://localhost:8080/api/apps/1/origins \
  -H 'Authorization: Bearer <user-jwt>' \
  -H 'Content-Type: application/json' \
  -d '{"allowed_origins":["https://app.example.com","http://localhost:3000"]}'
```

Embed session tokens include `app_id` in the JWT. Browser REST + WebSocket requests from customer frontends are allowed when the request `Origin` matches that app's list (or a platform origin).

Preflight (`OPTIONS`) is supported for `Authorization`, `Content-Type`, `Idempotency-Key`, and `X-API-Key`.

Server-side `/v1/*` integrations with API keys are not subject to browser CORS.

## Developer API (server-side integrations)

For backend-only integrations, use API keys instead of user JWTs. See [openapi.yaml](openapi.yaml).

### Sandbox keys

Create a sandbox key for development — prefix `bx_test_`. Sandbox and live keys hit the **same API host** but operate in **isolated data lanes**:

| Key | Data lane | Billing | Webhooks | Limits |
|-----|-----------|---------|----------|--------|
| `bx_test_*` | App sandbox workspace | Not counted | Not delivered | Sandbox caps, stricter rate limit, key expiry, TTL cleanup |
| `bx_live_*` | App production workspace | Plan quotas apply | Delivered | Plan quotas apply |

Each app has two workspaces: `workspace_id` (live) and `sandbox_workspace_id` (test). `/v1/*` routes automatically use the lane matching the key. Session JWTs include `key_env` (`sandbox` \| `live`) so browser clients cannot cross lanes.

Sandbox keys are intentionally bounded even on the production API host: defaults are 100 users, 20 channels, 1000 retained messages, 30 requests/minute, 90-day key expiry, and 30-day message cleanup. Tune with `SANDBOX_MAX_USERS`, `SANDBOX_MAX_CHANNELS`, `SANDBOX_MAX_MESSAGES`, `SANDBOX_API_RATE_LIMIT_PER_MINUTE`, `SANDBOX_API_KEY_TTL_DAYS`, and `SANDBOX_DATA_TTL_DAYS`.

```bash
curl -s -X POST http://localhost:8080/api/apps/1/keys \
  -H 'Authorization: Bearer <user-jwt>' \
  -H 'Content-Type: application/json' \
  -d '{"name":"dev","sandbox":true}'
```

Use sandbox keys for local, CI, and staging testing against production or staging API URLs. Ship `bx_live_*` keys in production backends only.

### Example: provision user + send message

```bash
API_KEY=bx_test_...

curl -s -X POST http://localhost:8080/v1/users \
  -H "X-API-Key: $API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"external_id":"sdk-user-1","username":"sdk_user"}'

curl -s -X POST http://localhost:8080/v1/channels \
  -H "X-API-Key: $API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"name":"general"}'

curl -s -X POST http://localhost:8080/v1/messages \
  -H "X-API-Key: $API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"channel_id":1,"external_id":"sdk-user-1","content":"Hello SDK"}'
```

### Example: embed session token (frontend SDK)

After provisioning a user, your backend issues a JWT for `BrenoxClient`:

```bash
curl -s -X POST http://localhost:8080/v1/sessions \
  -H "X-API-Key: $API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"external_id":"sdk-user-1","channel_id":1}'
```

Response:

```json
{
  "token": "eyJhbG...",
  "workspace_id": 1,
  "channel_id": 1,
  "user": { "id": 2, "external_id": "sdk-user-1", "username": "sdk_user" }
}
```

Return `token` to your frontend — never expose the API key in the browser.

## WebSocket send flow (curl + wscat)

```bash
# 1. Login
TOKEN=$(curl -s -X POST http://localhost:8080/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"email":"user@example.com","password":"secret123"}' | jq -r .token)

# 2. Connect (separate terminal)
wscat -c "ws://localhost:8080/api/ws?workspace_id=1&channel_id=1&token=$TOKEN"

# 3. Send message
{"type":"message.send","payload":{"content":"hello from SDK guide"}}
```

## Related docs

| Doc | Content |
|-----|---------|
| [WEBSOCKET_EVENTS.md](WEBSOCKET_EVENTS.md) | Event types |
| [WEBRTC_CLIENT.md](WEBRTC_CLIENT.md) | Voice/video calls |
| [openapi.yaml](openapi.yaml) | Public Developer API |
| [DEPLOYMENT.md](DEPLOYMENT.md) | Multi-instance + Redis |
