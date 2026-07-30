-- name: UpdateAppAllowedOrigins :one
UPDATE apps
SET allowed_origins = $2
WHERE id = $1
RETURNING *;

-- name: ListAppOriginEntries :many
SELECT id, workspace_id, sandbox_workspace_id, allowed_origins
FROM apps;

-- name: CreateApp :one
INSERT INTO apps (
    name,
    slug,
    workspace_id,
    sandbox_workspace_id,
    owner_id
)
VALUES (
    $1,
    $2,
    $3,
    $4,
    $5
)
RETURNING *;

-- name: GetAppByID :one
SELECT *
FROM apps
WHERE id = $1;

-- name: GetAppBySlug :one
SELECT *
FROM apps
WHERE slug = $1;

-- name: ListAppsByOwner :many
SELECT *
FROM apps
WHERE owner_id = $1
ORDER BY created_at DESC;

-- name: CreateAPIKey :one
INSERT INTO api_keys (
    app_id,
    name,
    key_prefix,
    key_hash,
    is_sandbox,
    expires_at
)
VALUES (
    $1,
    $2,
    $3,
    $4,
    $5,
    $6
)
RETURNING *;

-- name: GetAPIKeyByPrefix :one
SELECT *
FROM api_keys
WHERE key_prefix = $1
  AND revoked_at IS NULL;

-- name: ListAPIKeysByApp :many
SELECT *
FROM api_keys
WHERE app_id = $1
ORDER BY created_at DESC;

-- name: RevokeAPIKey :one
UPDATE api_keys
SET revoked_at = NOW()
WHERE id = $1
  AND app_id = $2
  AND revoked_at IS NULL
RETURNING *;

-- name: TouchAPIKeyLastUsed :exec
UPDATE api_keys
SET last_used_at = NOW()
WHERE id = $1;

-- name: CountSandboxAppUsers :one
SELECT COUNT(*)::BIGINT
FROM app_users
WHERE app_id = $1
  AND environment = 'sandbox';

-- name: CountSandboxWorkspaceChannels :one
SELECT COUNT(*)::BIGINT
FROM channels
WHERE workspace_id = $1;

-- name: CountWorkspaceMessages :one
SELECT COUNT(*)::BIGINT
FROM messages m
JOIN channels c ON c.id = m.channel_id
WHERE c.workspace_id = $1;

-- name: DeleteExpiredSandboxMessages :execrows
DELETE FROM messages m
USING channels c, apps a
WHERE m.channel_id = c.id
  AND c.workspace_id = a.sandbox_workspace_id
  AND m.created_at < $1;

-- name: DeleteExpiredEmptySandboxChannels :execrows
DELETE FROM channels c
USING apps a
WHERE c.workspace_id = a.sandbox_workspace_id
  AND c.created_at < $1
  AND NOT EXISTS (
    SELECT 1
    FROM messages m
    WHERE m.channel_id = c.id
  );

-- name: CreateAppUser :one
INSERT INTO app_users (
    app_id,
    user_id,
    external_id,
    environment
)
VALUES (
    $1,
    $2,
    $3,
    $4
)
RETURNING *;

-- name: GetAppUserByExternalID :one
SELECT *
FROM app_users
WHERE app_id = $1
  AND external_id = $2
  AND environment = $3;

-- name: GetAppUserByUserID :one
SELECT *
FROM app_users
WHERE app_id = $1
  AND user_id = $2
  AND environment = $3;

-- name: CreateWebhook :one
INSERT INTO webhooks (
    app_id,
    url,
    events,
    secret
)
VALUES (
    $1,
    $2,
    $3,
    $4
)
RETURNING *;

-- name: ListWebhooksByApp :many
SELECT *
FROM webhooks
WHERE app_id = $1
  AND disabled_at IS NULL
ORDER BY created_at DESC;

-- name: DisableWebhook :execrows
UPDATE webhooks
SET disabled_at = NOW()
WHERE id = $1
  AND app_id = $2
  AND disabled_at IS NULL;

-- name: GetIdempotencyKey :one
SELECT *
FROM idempotency_keys
WHERE app_id = $1
  AND idempotency_key = $2;

-- name: CreateIdempotencyKey :one
INSERT INTO idempotency_keys (
    app_id,
    idempotency_key,
    endpoint,
    status_code,
    response_body
)
VALUES (
    $1,
    $2,
    $3,
    $4,
    $5
)
RETURNING *;
