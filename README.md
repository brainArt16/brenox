# Brenox — Realtime Communication Platform

Go backend for a reusable realtime communication infrastructure (workspaces, channels, messages, WebSocket events, presence). Uses Gin, PostgreSQL, sqlc, and gorilla/websocket.

## Documentation

| Doc | Purpose |
|-----|---------|
| [docs/BACKEND_TASKS.md](docs/BACKEND_TASKS.md) | Full task tracker and roadmap |
| [docs/postman/](docs/postman/) | Postman collection for HTTP API |
| [AGENTS.md](AGENTS.md) | Agent roles for doc sync (task tracker, README, Postman) |
| [docs/WEBSOCKET_EVENTS.md](docs/WEBSOCKET_EVENTS.md) | WebSocket event catalog |
| [docs/WEBRTC.md](docs/WEBRTC.md) | Voice/video call signaling + TURN/STUN client config |
| [docs/WEBRTC_CLIENT.md](docs/WEBRTC_CLIENT.md) | SDK integration guide for WebRTC clients |
| [docs/VERSIONING.md](docs/VERSIONING.md) | Engine release semver + API contract versioning |
| [docs/openapi.yaml](docs/openapi.yaml) | Public Developer API OpenAPI spec |
| [docs/SDK_INTEGRATION.md](docs/SDK_INTEGRATION.md) | SDK auth, WebSocket, reconnection guide |
| [docs/PERMISSIONS.md](docs/PERMISSIONS.md) | Role-based permission matrix |
| [docs/DEPLOYMENT.md](docs/DEPLOYMENT.md) | Multi-instance topology, Redis, load balancer |
| [docs/KUBERNETES.md](docs/KUBERNETES.md) | Kubernetes deploy (Kustomize overlays) |
| [docs/RUNBOOK.md](docs/RUNBOOK.md) | Deploy, rollback, incident response |
| [docs/SECRETS.md](docs/SECRETS.md) | Secrets management |
| [docs/SECURITY.md](docs/SECURITY.md) | Security review notes |
| [docs/LOAD_TEST.md](docs/LOAD_TEST.md) | Load test baselines + k6 script |

## Repo layout

```text
brenox-engine/
├── cmd/
│   └── api/                        # Application entrypoint
├── internal/
│   ├── auth/                       # Registration, login, JWT
│   ├── authz/                      # Role-based permission checks
│   ├── workspaces/                 # Workspace CRUD + member admin
│   ├── channels/                   # Channel CRUD (workspace-scoped)
│   ├── chat/                       # Message persistence
│   ├── realtime/                   # WebSocket hub, Redis broker
│   ├── presence/                   # Redis-backed presence store + API
│   ├── notifications/              # Notification persistence + dispatch
│   ├── storage/                    # S3/MinIO object storage
│   ├── attachments/                # File uploads + message attachments
│   ├── calls/                      # Voice/video call rooms + WebRTC signaling
│   ├── apps/                       # Developer apps + API key management
│   ├── developerapi/               # Public /v1 API for third-party integrations
│   ├── users/                      # User profile API
│   ├── platformadmin/            # Platform admin API (cross-tenant)
│   ├── webhooks/                   # Webhook delivery dispatcher
│   ├── ratelimit/                  # API key rate limiting
│   ├── redis/                      # Redis client wrapper
│   ├── health/                     # Health check handler
│   ├── version/                    # Engine release + API contract version
│   ├── database/                   # Postgres pool
│   ├── db/                         # sqlc-generated queries
│   ├── metrics/                    # Prometheus metrics
│   └── middleware/                 # Auth, CORS, rate limits, audit, security headers
├── pkg/
│   └── jwt/                        # JWT helpers
├── sql/
│   ├── migrations/                 # Schema migrations
│   └── queries/                    # sqlc query definitions
├── deploy/                         # Kubernetes manifests (Kustomize)
│   ├── base/                       # API Deployment, Service, migrate Job
│   ├── overlays/
│   │   ├── dev/                    # In-cluster Postgres, Redis, MinIO
│   │   └── prod/                   # Managed DB/Redis/S3 + Ingress
│   └── Dockerfile.migrate          # Migrate image (bundles sql/migrations)
├── docs/
│   ├── BACKEND_TASKS.md            # Task tracker
│   ├── KUBERNETES.md               # K8s deploy guide
│   ├── postman/                    # API collection
│   └── …                           # WebSocket, WebRTC, OpenAPI, runbooks
├── .github/
│   └── workflows/                  # CI + container image builds (GHCR)
├── Dockerfile                      # API image
├── docker-compose.dev.yaml         # Local dev — all services in Docker
├── docker-compose.yaml  # Test + prod deploy (env differs)
├── Makefile
└── sqlc.yaml
```

## Quick start

Prerequisites: Docker, Make (Go optional if you only use Docker).

### Development (all services in Docker)

```bash
make dev-up          # API + Postgres + Redis + MinIO + migrations
curl http://localhost:8080/health
curl http://localhost:8080/version
```

**Production:** `https://api.breno-x.com` (see [docs/VERSIONING.md](docs/VERSIONING.md)).

Services: API `:8080`, Postgres `:5432`, Redis `:6379`, MinIO `:9000` (console `:9001`).  
Default dev credentials — override via `.env` (copy from `.env.example`).

```bash
make dev-logs        # tail API logs
make dev-down        # stop stack
make migrate-dev     # re-run migrations after new migration files
```

### Local API process (services still in Docker)

```bash
make dev-up          # starts infra + can skip rebuilding api if down api container
docker compose -f docker-compose.dev.yaml stop api
cp .env.example .env
make run
```

### Test / production

Postgres, Redis, and S3 are managed services — set endpoints in the env file.

```bash
cp .env.test.example .env.test
# Edit DB_HOST, REDIS_URL, S3_* for your test providers
make migrate-test
make test-up

cp .env.prod.example .env.prod
# Edit DB_HOST, REDIS_URL, S3_* — Coolify internal Postgres: DB_SSLMODE=disable
make prod-up          # migrations run automatically on API container start
```

Optional: `make migrate-prod` before `prod-up` if you want migrations applied before the API starts. Set `RUN_MIGRATIONS_ON_START=false` to skip in-container migrate.

See [docs/RUNBOOK.md](docs/RUNBOOK.md) and [docs/SECRETS.md](docs/SECRETS.md).

### Kubernetes (local cluster)

```bash
make k8s-cluster-create   # first time only — creates kind cluster "brenox"
make k8s-build k8s-load-kind k8s-dev-up
curl http://localhost:30080/health
```

See [docs/KUBERNETES.md](docs/KUBERNETES.md) and [deploy/](deploy/) for overlays, production setup, and migrations.

### Tests

```bash
make test
```

API listens on `:8080`. See [docs/DEPLOYMENT.md](docs/DEPLOYMENT.md) for multi-instance Redis setup.

### Existing databases

Migration `000004_add_workspaces` backfills a **Default Workspace** per channel owner. For a clean slate in dev:

```bash
docker compose -f docker-compose.dev.yaml down -v
make dev-up
```

## API overview

All channel and message routes are scoped under a workspace.

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| GET | `/health` | No | DB + Redis health probe |
| GET | `/version` | No | Engine release + API contract version |
| GET | `/metrics` | No | Prometheus metrics |
| POST | `/auth/register` | No | Create account |
| POST | `/auth/login` | No | Login, returns JWT |
| POST | `/auth/refresh` | No | Refresh JWT (valid or recently expired token) |
| POST | `/api/workspaces` | JWT | Create workspace |
| GET | `/api/workspaces` | JWT | List user workspaces |
| GET | `/api/workspaces/:workspace_id` | JWT | Workspace detail |
| GET | `/api/workspaces/:workspace_id/members` | JWT | List members |
| POST | `/api/workspaces/:workspace_id/members` | JWT | Invite member (owner/admin) |
| DELETE | `/api/workspaces/:workspace_id/members/:user_id` | JWT | Remove member |
| PATCH | `/api/workspaces/:workspace_id/members/:user_id` | JWT | Change member role |
| POST | `/api/workspaces/:workspace_id/channels` | JWT | Create channel (role gated) |
| GET | `/api/workspaces/:workspace_id/channels` | JWT | List workspace channels |
| POST | `/api/workspaces/:workspace_id/channels/:id/join` | JWT | Join channel |
| POST | `/api/workspaces/:workspace_id/channels/:id/leave` | JWT | Leave channel |
| POST | `/api/uploads` | JWT | Get presigned upload URL |
| POST | `/api/workspaces/:workspace_id/channels/:id/messages` | JWT | Send message (optional `attachments`) |
| GET | `/api/workspaces/:workspace_id/channels/:id/messages` | JWT | Message history |
| POST | `/api/workspaces/:workspace_id/channels/:id/messages/:message_id/attachments` | JWT | Attach files to message |
| GET | `/api/workspaces/:workspace_id/channels/:id/messages/:message_id/attachments` | JWT | List message attachments (`url` is API content path) |
| GET | `/api/workspaces/:workspace_id/channels/:id/messages/:message_id/attachments/:attachment_id/content` | JWT | Stream attachment bytes (hides object-storage path) |
| GET | `/api/notifications` | JWT | List notifications (`?limit=&offset=`) |
| PATCH | `/api/notifications/:id/read` | JWT | Mark notification read |
| POST | `/api/notifications/read-all` | JWT | Mark all notifications read |
| GET | `/api/presence` | JWT | Globally online users (status, last_seen) |
| GET | `/api/workspaces/:workspace_id/presence` | JWT | Online members in workspace |
| GET | `/api/users/me` | JWT | Current user profile (`platform_role`, `suspended`) |
| PATCH | `/api/users/me` | JWT | Update username |
| PATCH | `/api/users/me/password` | JWT | Change password (requires current password) |
| GET | `/api/users/me/status` | JWT | Current presence status |
| PATCH | `/api/users/me/status` | JWT | Set presence status (`online`, `away`, `offline`) |
| GET | `/api/admin/overview` | JWT (support+) | Platform counts (users, workspaces, apps) |
| GET | `/api/admin/users` | JWT (support+) | List/search platform users |
| GET | `/api/admin/users/:id` | JWT (support+) | User detail |
| PATCH | `/api/admin/users/:id` | JWT (admin) | Suspend/unsuspend or change platform role |
| GET | `/api/admin/workspaces` | JWT (support+) | List all workspaces |
| GET | `/api/admin/workspaces/:id` | JWT (support+) | Workspace detail |
| GET | `/api/admin/workspaces/:id/members` | JWT (support+) | Workspace members (cross-tenant) |
| GET | `/api/admin/apps` | JWT (support+) | List all developer apps |
| GET | `/api/admin/apps/:id` | JWT (support+) | App detail |
| GET | `/api/admin/apps/:app_id/keys` | JWT (support+) | List API keys for any app |
| DELETE | `/api/admin/apps/:app_id/keys/:key_id` | JWT (admin) | Emergency cross-tenant API key revoke |
| GET | `/api/plans` | No | Active plan tiers (limits + feature flags) |
| GET/PATCH/DELETE | `/api/admin/plans` | JWT (support+/admin write) | Manage plans, Stripe price IDs, feature toggles |
| GET | `/api/platform/status` | No | Maintenance mode status |
| POST | `/webhooks/stripe` | Stripe signature | Subscription lifecycle webhooks |
| GET | `/api/apps/:app_id/billing` | JWT (owner) | App subscription + usage |
| POST | `/api/apps/:app_id/billing/checkout` | JWT (owner) | Create Stripe Checkout session |
| GET | `/api/admin/billing/overview` | JWT (support+) | Active subscription count |
| GET | `/api/admin/billing/subscriptions` | JWT (support+) | All app subscriptions + usage |
| PATCH | `/api/admin/apps/:id/subscription` | JWT (admin) | Override plan/status |
| GET/PATCH | `/api/admin/platform-settings` | JWT (support+/admin write) | Maintenance mode |
| GET | `/api/admin/audit-logs` | JWT (support+) | Audit log entries (`?user_id=`, `?action=`, pagination) |
| POST | `/api/workspaces/:workspace_id/channels/:id/calls` | JWT | Initiate call (`mode`: `voice` or `video`) |
| POST | `/api/calls/:id/join` | JWT | Join call (channel members only) |
| POST | `/api/calls/:id/leave` | JWT | Leave call |
| POST | `/api/apps` | JWT | Create developer app (live + sandbox workspaces) |
| GET | `/api/apps` | JWT | List your developer apps |
| GET | `/api/apps/:app_id` | JWT | Get app by ID (owner only) |
| PATCH | `/api/apps/:app_id/origins` | JWT | Update per-app browser allowed origins |
| POST | `/api/apps/:app_id/keys` | JWT | Create API key (secret shown once) |
| GET | `/api/apps/:app_id/keys` | JWT | List API keys for app |
| DELETE | `/api/apps/:app_id/keys/:key_id` | JWT | Revoke API key |
| POST | `/api/apps/:app_id/webhooks` | JWT | Register webhook endpoint |
| GET | `/api/apps/:app_id/webhooks` | JWT | List webhooks for app |
| DELETE | `/api/apps/:app_id/webhooks/:webhook_id` | JWT | Disable webhook |
| POST | `/v1/users` | API key | Provision app-scoped user |
| POST | `/v1/sessions` | API key | Issue user JWT for embed clients (optional channel join) |
| POST | `/v1/channels` | API key | Create channel in app workspace |
| POST | `/v1/messages` | API key | Send message |
| GET | `/v1/messages?channel_id=` | API key | List channel messages |
| GET | `/api/ws?workspace_id=&channel_id=` | JWT (header or `?token=`) | WebSocket upgrade |

WebSocket auth accepts `Authorization: Bearer …` or `?token=` on the upgrade URL. Connection limits and allowed origins are configurable via env (see `.env.example`). Set `PLATFORM_ADMIN_EMAILS` (comma-separated) to bootstrap platform admins. Graceful shutdown closes active WebSocket connections on SIGTERM.

Voice and video call signaling (`call.offer`, `call.answer`, `call.ice`, `call.video.*`, etc.) is sent over the channel WebSocket. See [docs/WEBRTC.md](docs/WEBRTC.md) and [docs/WEBRTC_CLIENT.md](docs/WEBRTC_CLIENT.md).

**Developer API:** Authenticate with `Authorization: Bearer bx_live_...` / `bx_test_...` or `X-API-Key`. Create apps/keys via JWT routes. Sandbox keys use the same API host but are capped by `SANDBOX_MAX_USERS`, `SANDBOX_MAX_CHANNELS`, `SANDBOX_MAX_MESSAGES`, `SANDBOX_API_RATE_LIMIT_PER_MINUTE`, `SANDBOX_API_KEY_TTL_DAYS`, and `SANDBOX_DATA_TTL_DAYS`. See [docs/openapi.yaml](docs/openapi.yaml).

Allowed browser origins must be HTTPS hostnames. Local development may use `localhost`, `127.0.0.1`, or `[::1]` origins over HTTP; other IP-address origins are rejected.

Channel names are unique **per workspace**.

Import [docs/postman/brenox.postman_collection.json](docs/postman/brenox.postman_collection.json) for request examples.

## Development

- SQL lives in `sql/queries` and `sql/migrations`.
- Regenerate sqlc: `make sqlc` or `sqlc generate`
- New migration: `make migration <name>`
- Handlers orchestrate HTTP only; business logic in services; DB via sqlc.

After backend changes, run the documentation agents (see [AGENTS.md](AGENTS.md)).

## Testing

Add unit tests alongside packages. Use Testcontainers or `make db-start` for integration tests.

## License

No license file yet.
