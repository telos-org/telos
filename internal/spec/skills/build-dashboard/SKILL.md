---
name: build-dashboard
description: |
  Build and deploy a live per-service operations dashboard. The dashboard is
  a React app with an Express API backend, deployed as an internal ClusterIP
  Service and published through a Telos public route.
  It queries kubectl and service endpoints on every request — a live control
  plane, not a static snapshot. Styled with the Telos design system.
metadata:
  category: frontend
  author: telos
allowed-tools: Bash(kubectl:*) Bash(npm:*) Bash(npx:*) Bash(node:*)
---

# Build Dashboard

Build a live operations dashboard for a deployed service. The dashboard is a
**live control plane** — the API queries kubectl and the service on every
request, and the frontend auto-refreshes every 10 seconds.

## Architecture

```
Express API server  → queries kubectl + service endpoints live → serves JSON
Vite React frontend → fetches /api/* every 10s                → renders state
Both served from one Deployment behind a ClusterIP Service
Cloudflared route   → publishes the dashboard at a browser URL
```

1. Build an Express API that queries kubectl and the service on every request
2. Build a Vite React frontend that fetches from the API and auto-refreshes
3. Deploy both in a single Deployment (API serves the static files too)
4. Expose via a Service named `dashboard` with type ClusterIP
5. Publish via a route ConfigMap labeled `telos.ai/public-route=primary`

**Critical:** The service MUST be named `dashboard`, and the route ConfigMap
MUST have `data.type=dashboard`. The Telos operator console uses the allocated
route hostname as the iframe URL.

**Critical:** No static JSON snapshots. All data is queried live on each API
request.

## Design System

Use the Telos design tokens. Reference implementation in `reference/tokens.js`.

```
Colors (dark theme):
  --t-void: #0a1220        (deepest background)
  --t-abyss: #0f1829       (container background)
  --t-ink: #162236          (card background)
  --t-structure: #283c58    (borders)
  --t-mid: #4d6580          (secondary text)
  --t-muted: #7e93aa        (labels)
  --t-soft: #a8bace         (body text)
  --t-light: #dae2ed        (primary text)
  --t-primary: #3b82f6      (accent — blue)
  --t-signal: #4ade80       (success — green)
  --t-warm: #60a5fa         (warning — warm blue)
  --t-danger: #f87171       (error — red)

Typography:
  Data/values: "JetBrains Mono", monospace (load from CDN)
  Labels: system-ui, -apple-system, sans-serif
  Sizes: 0.7rem for values, 0.6rem for labels, 1.1rem for headings

Layout:
  Dark background: var(--t-abyss) or #0f1829
  Cards: background rgba(40,60,88,0.1), border 1px solid rgba(40,60,88,0.18), border-radius 4px
  Spacing: 12px padding in cards, 16px gap between sections
```

## Dashboard Sections

Adapt sections based on what the service actually has. Not all apply to every
service. Inspect what's deployed and include only relevant sections.

### Universal sections (always include):

1. **Header** — Service name, namespace, status dot, last-updated timestamp
2. **Health** — Pod status grid (name, phase, ready, restarts, image)
3. **Networking** — Service type, ClusterIP, ports, public handle
4. **Events** — Recent K8s events (last 10, warnings highlighted)

### Service-specific sections (include when relevant):

**Databases (PostgreSQL, MySQL, MongoDB, FerretDB):**
- Connection info — DSN, host, port, user, password (masked with reveal)
- Replication status — primary/replica roles, lag, sync state
- Key metrics — active connections, queries/sec, cache hit ratio
- Database objects — schemas, tables, row counts
- Configuration — key tuning parameters (work_mem, shared_buffers, etc.)

**Caches (Redis, Memcached):**
- Connection info — host, port, auth
- Memory — used/max, eviction rate, hit ratio
- Key stats — total keys, expired keys, keyspace info

**Message queues (Kafka):**
- Broker list, topic count, partition info
- Consumer group lag
- Throughput — messages/sec in/out

**Search (OpenSearch, Elasticsearch):**
- Cluster health — green/yellow/red, node count
- Index stats — document count, store size
- Query latency

**Vault / secret managers:**
- Seal status, HA mode
- Auth methods configured
- Secret engine mounts

### Sensitive Data

- Passwords and DSNs must be masked by default (show `••••••••••`)
- Add a show/hide toggle button
- Copy button copies the real value, not the mask
- Never log or print passwords during the build

## API Design

The Express API should have endpoints for each section:

```
GET /api/health     → { pods: [...], services: [...] }
GET /api/connection → { dsn, host, port, user, password, ... }
GET /api/metrics    → { ... service-specific metrics ... }
GET /api/events     → { events: [...] }
```

Each endpoint runs kubectl or service queries live. Example:

```bash
# Pods
kubectl get pods -n $NAMESPACE -o json | jq '[.items[] | {
  name: .metadata.name, phase: .status.phase,
  ready: (.status.conditions // [] | map(select(.type=="Ready")) | .[0].status // "False"),
  restarts: (.status.containerStatuses[0].restartCount // 0),
  image: .status.containerStatuses[0].image
}]'

# Services
kubectl get svc -n $NAMESPACE -o json | jq '[.items[] |
  select(.metadata.name != "kubernetes") | {
  name: .metadata.name, type: .spec.type, clusterIP: .spec.clusterIP,
  ports: [.spec.ports[] | {port: .port, targetPort: .targetPort, protocol: .protocol}]
}]'

# Events (last 10)
kubectl get events -n $NAMESPACE --sort-by=.lastTimestamp -o json | jq '[
  .items[-10:] | .[] | {
  type: .type, reason: .reason, message: .message,
  object: .involvedObject.name, timestamp: .lastTimestamp
}]'
```

For service-specific metrics, connect to the service directly:

```bash
# PostgreSQL
PGPASSWORD=$PASS psql -h $HOST -U $USER -d $DB -c "SELECT ..."

# Redis
redis-cli -h $HOST INFO

# Kafka
kafka-topics.sh --bootstrap-server $HOST --describe
```

## Build Steps

### 1. Scaffold

```bash
mkdir -p /workspace/output/dashboard && cd /workspace/output/dashboard
npm create vite@latest . -- --template react
npm install express
```

### 2. Write the API (server.js)

Express server that:
- Runs kubectl and service queries on each request
- Serves the built React frontend from `dist/`
- Listens on port 3000

### 3. Write the React frontend (src/App.jsx)

- Fetches from `/api/*` endpoints
- Auto-refreshes every 10 seconds
- Uses Telos design tokens
- Adapts sections to the data available

### 4. Build and deploy

```bash
npm run build

# Create a Deployment running the Express server
kubectl apply -n $NAMESPACE -f - <<EOF
apiVersion: apps/v1
kind: Deployment
metadata:
  name: dashboard
spec:
  replicas: 1
  selector:
    matchLabels:
      app: dashboard
  template:
    metadata:
      labels:
        app: dashboard
    spec:
      serviceAccountName: default
      containers:
      - name: dashboard
        image: node:22-slim
        command: ["node", "server.js"]
        workingDir: /app
        ports:
        - containerPort: 3000
        volumeMounts:
        - name: app
          mountPath: /app
      volumes:
      - name: app
        configMap:
          name: dashboard-app
---
apiVersion: v1
kind: Service
metadata:
  name: dashboard
spec:
  type: ClusterIP
  selector:
    app: dashboard
  ports:
  - port: 3000
    targetPort: 3000
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: dashboard-route
  labels:
    telos.ai/public-route: primary
data:
  type: dashboard
  prefix: dashboard-${NAMESPACE#ns-}
  service: http://dashboard.$NAMESPACE.svc.cluster.local:3000
EOF
```

Package the built app + server as a ConfigMap:
```bash
kubectl create configmap dashboard-app \
  --from-file=server.js \
  --from-file=dist/ \
  -n $NAMESPACE
```

## Component Patterns

Reference implementations are in `reference/components.jsx`. Key patterns:
SecretValue (masked password with reveal + copy), StatusDot, Card wrapper,
PodRow, CertDownload.

## Verification

- `dashboard-route` receives a `hostname` from the route reconciler
- Dashboard loads at the allocated route URL
- Data refreshes every 10 seconds
- Pod health matches `kubectl get pods` output
- Connection info matches actual service secrets
- Passwords masked by default, revealable
- All Telos styling applied (dark theme, correct fonts and colors)
- Service is named `dashboard` and the route ConfigMap has `type=dashboard`
