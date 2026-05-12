---
name: keycloak-admin
description: |
  Keycloak Admin REST API. Use when configuring or mutating a Keycloak
  instance — realms, clients, users, roles, identity providers, SMTP,
  sessions, events. The authoritative surface for every Keycloak config
  change. Never mutate Keycloak by editing the Deployment manifest.
  Triggers on: keycloak, realm, oidc client, identity, first admin, IdP.
metadata:
  category: identity
  protocols: oidc
  author: telos
allowed-tools: Bash(kubectl:*) Bash(curl:*) Bash(jq:*)
---

# Keycloak Admin

You are configuring a Keycloak instance through its Admin REST API.
This is the authoritative surface for realm config, clients, users,
roles, identity providers, SMTP, sessions, and events. The Deployment
manifest is not.

## First principle

Keycloak's **content** is the Admin REST API. Keycloak's **shape**
(Deployment, Service, PVC) is the Kubernetes spec. A config change —
add a client, invite a user, rotate a secret, flip brute-force
protection — is always an API call, never a pod mutation. If you find
yourself editing the Keycloak Deployment manifest for anything other
than image version, resource limits, or readiness probes, stop.

Unlike k8s manifests (workspace-is-source-of-truth), Keycloak realm
state is Admin-API-authoritative. The workspace is not the source of
truth for realm config — the live server is. Always read-modify-write
against the API; never assume the in-workspace copy is current.

## Authentication

Keycloak's admin account lives in the `master` realm. For a Telos
deployment the credentials are in the runtime secret named
`keycloak-admin` (keys: `username`, `password`). Set
`KEYCLOAK_NAMESPACE` from the live run context; if it is absent,
discover the namespace that owns the `keycloak-admin` secret.

```bash
KEYCLOAK_NAMESPACE=${KEYCLOAK_NAMESPACE:-$(kubectl get secret -A -o json \
  | jq -r '.items[] | select(.metadata.name=="keycloak-admin") | .metadata.namespace' \
  | head -1)}
ADMIN_USER=$(kubectl -n "$KEYCLOAK_NAMESPACE" get secret keycloak-admin -o jsonpath='{.data.username}' | base64 -d)
ADMIN_PASS=$(kubectl -n "$KEYCLOAK_NAMESPACE" get secret keycloak-admin -o jsonpath='{.data.password}' | base64 -d)

# In-cluster: reach Keycloak via its runtime service name
KC=http://keycloak.${KEYCLOAK_NAMESPACE}.svc.cluster.local:8080

# Out-of-cluster: port-forward
kubectl -n "$KEYCLOAK_NAMESPACE" port-forward svc/keycloak 8080:8080 &
KC=http://localhost:8080

TOKEN=$(curl -s -X POST "$KC/realms/master/protocol/openid-connect/token" \
  -d "client_id=admin-cli" \
  -d "username=$ADMIN_USER" \
  -d "password=$ADMIN_PASS" \
  -d "grant_type=password" | jq -r .access_token)

KCAPI="$KC/admin/realms"
AUTH="Authorization: Bearer $TOKEN"
```

Admin tokens on `master`/`admin-cli` expire in ~60s. Refresh before
long sequences. A 401 mid-sequence does not mean the previous call
failed — re-fetch the token and verify the change landed before
retrying.

## Realms

```bash
# List realms
curl -s -H "$AUTH" "$KCAPI" | jq -r '.[].realm'

# Create realm (telos shape)
curl -s -X POST -H "$AUTH" -H "Content-Type: application/json" "$KC/admin/realms" -d '{
  "realm":"telos",
  "displayName":"Telos",
  "enabled":true,
  "registrationAllowed":false,
  "loginWithEmailAllowed":true,
  "emailAsUsername":true,
  "ssoSessionIdleTimeout":1800,
  "ssoSessionMaxLifespan":43200,
  "accessTokenLifespan":300
}'

# Get realm config
curl -s -H "$AUTH" "$KCAPI/telos" | jq .

# Update realm (read-modify-write — PUT takes the full document)
curl -s -H "$AUTH" "$KCAPI/telos" \
  | jq '.bruteForceProtected = true | .failureFactor = 5 | .waitIncrementSeconds = 60 | .maxFailureWaitSeconds = 900' \
  | curl -s -X PUT -H "$AUTH" -H "Content-Type: application/json" "$KCAPI/telos" -d @-
```

## Clients

```bash
# List clients
curl -s -H "$AUTH" "$KCAPI/telos/clients" \
  | jq -r '.[] | "\(.clientId)\t\(.publicClient)\t\(.serviceAccountsEnabled // false)"'

# Confidential client with service account (telos-api shape)
curl -s -X POST -H "$AUTH" -H "Content-Type: application/json" "$KCAPI/telos/clients" -d '{
  "clientId":"telos-api",
  "protocol":"openid-connect",
  "publicClient":false,
  "serviceAccountsEnabled":true,
  "standardFlowEnabled":false,
  "directAccessGrantsEnabled":false,
  "clientAuthenticatorType":"client-secret"
}'

# Public SPA client with PKCE (telos-frontend shape)
curl -s -X POST -H "$AUTH" -H "Content-Type: application/json" "$KCAPI/telos/clients" -d '{
  "clientId":"telos-frontend",
  "protocol":"openid-connect",
  "publicClient":true,
  "standardFlowEnabled":true,
  "directAccessGrantsEnabled":false,
  "redirectUris":["http://localhost:5173/*"],
  "webOrigins":["http://localhost:5173"],
  "attributes":{"pkce.code.challenge.method":"S256"}
}'

# Public client with Device Authorization Grant (telos-cli shape)
curl -s -X POST -H "$AUTH" -H "Content-Type: application/json" "$KCAPI/telos/clients" -d '{
  "clientId":"telos-cli",
  "protocol":"openid-connect",
  "publicClient":true,
  "standardFlowEnabled":false,
  "directAccessGrantsEnabled":false,
  "attributes":{"oauth2.device.authorization.grant.enabled":"true"}
}'

# Resolve clientId → internal UUID
CID=$(curl -s -H "$AUTH" "$KCAPI/telos/clients?clientId=telos-api" | jq -r '.[0].id')

# Read current client secret
curl -s -H "$AUTH" "$KCAPI/telos/clients/$CID/client-secret" | jq -r .value

# Rotate client secret
NEW_SECRET=$(curl -s -X POST -H "$AUTH" "$KCAPI/telos/clients/$CID/client-secret" | jq -r .value)

# Persist to the runtime secret downstream consumers read
kubectl -n "$KEYCLOAK_NAMESPACE" patch secret telos-api-oidc --type merge \
  -p "{\"stringData\":{\"client_secret\":\"$NEW_SECRET\"}}"

# Add a redirect URI (read-modify-write)
curl -s -H "$AUTH" "$KCAPI/telos/clients/$CID" \
  | jq '.redirectUris += ["https://auth-03ca.usetelos.ai/*"]' \
  | curl -s -X PUT -H "$AUTH" -H "Content-Type: application/json" "$KCAPI/telos/clients/$CID" -d @-
```

## Users

```bash
# Search / count
curl -s -H "$AUTH" "$KCAPI/telos/users?search=rohan" \
  | jq -r '.[] | "\(.id)\t\(.username)\t\(.email)"'
curl -s -H "$AUTH" "$KCAPI/telos/users/count"

# Create user
curl -s -X POST -H "$AUTH" -H "Content-Type: application/json" "$KCAPI/telos/users" -d '{
  "username":"rohan@example.com",
  "email":"rohan@example.com",
  "firstName":"Rohan",
  "lastName":"Gupta",
  "enabled":true,
  "emailVerified":true
}'

UID=$(curl -s -H "$AUTH" "$KCAPI/telos/users?username=rohan@example.com" | jq -r '.[0].id')

# Set temporary password (user must change on next login)
curl -s -X PUT -H "$AUTH" -H "Content-Type: application/json" \
  "$KCAPI/telos/users/$UID/reset-password" \
  -d '{"type":"password","value":"<temp>","temporary":true}'

# Assign realm role
ROLE=$(curl -s -H "$AUTH" "$KCAPI/telos/roles/admin")
curl -s -X POST -H "$AUTH" -H "Content-Type: application/json" \
  "$KCAPI/telos/users/$UID/role-mappings/realm" -d "[$ROLE]"

# List a user's realm roles
curl -s -H "$AUTH" "$KCAPI/telos/users/$UID/role-mappings/realm" | jq -r '.[].name'

# Send password-reset email (requires SMTP configured)
curl -s -X PUT -H "$AUTH" -H "Content-Type: application/json" \
  "$KCAPI/telos/users/$UID/execute-actions-email" -d '["UPDATE_PASSWORD"]'

# Disable user
curl -s -X PUT -H "$AUTH" -H "Content-Type: application/json" \
  "$KCAPI/telos/users/$UID" -d '{"enabled":false}'
```

## Realm roles

```bash
# List
curl -s -H "$AUTH" "$KCAPI/telos/roles" | jq -r '.[].name'

# Create
curl -s -X POST -H "$AUTH" -H "Content-Type: application/json" "$KCAPI/telos/roles" \
  -d '{"name":"operator","description":"run specs, view sessions"}'

# Make a role the default for new users (modern Keycloak composite model)
DEFAULT_ID=$(curl -s -H "$AUTH" "$KCAPI/telos" | jq -r .defaultRole.id)
VIEWER=$(curl -s -H "$AUTH" "$KCAPI/telos/roles/viewer")
curl -s -X POST -H "$AUTH" -H "Content-Type: application/json" \
  "$KCAPI/telos/roles-by-id/$DEFAULT_ID/composites" -d "[$VIEWER]"
```

## Identity providers

```bash
curl -s -H "$AUTH" "$KCAPI/telos/identity-provider/instances" | jq -r '.[].alias'

# Create GitHub IdP, disabled until creds are real
GH_ID=$(kubectl -n "$KEYCLOAK_NAMESPACE" get secret keycloak-github-idp -o jsonpath='{.data.GITHUB_CLIENT_ID}' | base64 -d)
GH_SECRET=$(kubectl -n "$KEYCLOAK_NAMESPACE" get secret keycloak-github-idp -o jsonpath='{.data.GITHUB_CLIENT_SECRET}' | base64 -d)

curl -s -X POST -H "$AUTH" -H "Content-Type: application/json" \
  "$KCAPI/telos/identity-provider/instances" -d "{
    \"alias\":\"github\",
    \"providerId\":\"github\",
    \"enabled\":false,
    \"trustEmail\":true,
    \"config\":{
      \"clientId\":\"$GH_ID\",
      \"clientSecret\":\"$GH_SECRET\",
      \"defaultScope\":\"user:email\",
      \"syncMode\":\"IMPORT\"
    }
  }"

# Flip enabled once creds are real
curl -s -H "$AUTH" "$KCAPI/telos/identity-provider/instances/github" \
  | jq '.enabled = true' \
  | curl -s -X PUT -H "$AUTH" -H "Content-Type: application/json" \
    "$KCAPI/telos/identity-provider/instances/github" -d @-
```

## SMTP

SMTP lives on the realm document under `smtpServer`.

```bash
SMTP_HOST=$(kubectl -n "$KEYCLOAK_NAMESPACE" get secret keycloak-smtp -o jsonpath='{.data.SMTP_HOST}' | base64 -d)
SMTP_PORT=$(kubectl -n "$KEYCLOAK_NAMESPACE" get secret keycloak-smtp -o jsonpath='{.data.SMTP_PORT}' | base64 -d)
SMTP_USER=$(kubectl -n "$KEYCLOAK_NAMESPACE" get secret keycloak-smtp -o jsonpath='{.data.SMTP_USER}' | base64 -d)
SMTP_PASS=$(kubectl -n "$KEYCLOAK_NAMESPACE" get secret keycloak-smtp -o jsonpath='{.data.SMTP_PASSWORD}' | base64 -d)
SMTP_FROM=$(kubectl -n "$KEYCLOAK_NAMESPACE" get secret keycloak-smtp -o jsonpath='{.data.SMTP_FROM}' | base64 -d)

curl -s -H "$AUTH" "$KCAPI/telos" \
  | jq ".smtpServer = {
      \"host\":\"$SMTP_HOST\",\"port\":\"$SMTP_PORT\",
      \"user\":\"$SMTP_USER\",\"password\":\"$SMTP_PASS\",
      \"from\":\"$SMTP_FROM\",\"auth\":\"true\",\"starttls\":\"true\"
    }" \
  | curl -s -X PUT -H "$AUTH" -H "Content-Type: application/json" "$KCAPI/telos" -d @-
```

Verify by invoking `execute-actions-email` against the first admin
(see Users section). A 204 plus message delivery is the proof.

## Sessions

```bash
# Active session count for a client
curl -s -H "$AUTH" "$KCAPI/telos/clients/$CID/user-sessions" | jq 'length'

# Active sessions for a user
curl -s -H "$AUTH" "$KCAPI/telos/users/$UID/sessions" | jq .

# Logout a single user
curl -s -X POST -H "$AUTH" "$KCAPI/telos/users/$UID/logout"

# Logout every user in the realm — nuclear, require an explicit reason
# in decisions.md before invoking
curl -s -X POST -H "$AUTH" "$KCAPI/telos/logout-all"
```

## Events

```bash
# Current config
curl -s -H "$AUTH" "$KCAPI/telos/events/config" | jq .

# Enable login + admin events with 7-day retention
curl -s -X PUT -H "$AUTH" -H "Content-Type: application/json" \
  "$KCAPI/telos/events/config" -d '{
    "eventsEnabled":true,
    "eventsExpiration":604800,
    "eventsListeners":["jboss-logging"],
    "enabledEventTypes":[],
    "adminEventsEnabled":true,
    "adminEventsDetailsEnabled":true
  }'

# Recent login failures
curl -s -H "$AUTH" "$KCAPI/telos/events?type=LOGIN_ERROR&max=50" \
  | jq -r '.[] | "\(.time)\t\(.ipAddress)\t\(.error)\t\(.details.username // "?")"'

# Recent admin mutations
curl -s -H "$AUTH" "$KCAPI/telos/admin-events?max=50" \
  | jq -r '.[] | "\(.time)\t\(.operationType)\t\(.resourcePath)"'
```

## Verifying a change

Every mutation is followed by a read-back that proves it landed:

```bash
# After creating telos-api: confirm it's confidential with service account
curl -s -H "$AUTH" "$KCAPI/telos/clients?clientId=telos-api" \
  | jq '.[0] | {publicClient, serviceAccountsEnabled, clientAuthenticatorType}'

# After assigning admin role: confirm the user carries it
curl -s -H "$AUTH" "$KCAPI/telos/users/$UID/role-mappings/realm" \
  | jq -r '.[].name' | grep -w admin
```

For OIDC-level verification, probe the public issuer from outside the
runtime environment:

```bash
KEYCLOAK_ISSUER=${KEYCLOAK_ISSUER:?set the public issuer, e.g. https://auth-example.usetelos.ai/realms/telos}
curl -s "$KEYCLOAK_ISSUER/.well-known/openid-configuration" \
  | jq '{issuer, token_endpoint, device_authorization_endpoint}'
```

## Rules

- **Never mutate the Keycloak Deployment manifest for config
  changes.** Realm, clients, users, roles, IdPs, SMTP, sessions,
  events — all REST API calls. Shape edits are reserved for image
  bumps, resource limits, and probes.
- **Read-modify-write for realm-level and client-level updates.**
  PUT accepts the full document and silently drops unknown fields.
  Fetch current, `jq`-patch, PUT back.
- **Record every admin mutation in `decisions.md`.** The admin
  event stream captures API-level history; `decisions.md` captures
  intent.
- **Secrets stay in the runtime secret store.** Client secrets, SMTP
  credentials, IdP credentials — read them from documented secret inputs, never
  echo into workspace files, ConfigMaps, or logs.
