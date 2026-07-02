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
allowed-tools: Bash(kubectl:*) Bash(npm:*) Bash(npx:*) Bash(node:*) Bash(tar:*)
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

Match the Telos console (the web app that embeds this dashboard in an iframe).
It uses the `@telos-org/design` system: a shadcn "neutral" palette with **both a
light and a dark mode**. The dashboard must look like it belongs inside that
console in **either** mode — same colors, fonts, and radii.

Colors and fonts are CSS variables defined in `reference/theme.css` (the
vendored `@telos-org/design` token surface, light + dark).
`reference/components.jsx` reads them via `var(--token)`. This is the single
source of truth — do not invent a palette, do not hardcode colors in
components, and do not use the old dark/navy theme.

### Theming — follow the console's light/dark toggle

The dashboard can't see the console's `.dark` class (separate document), so the
console passes the mode in: a `?theme=light|dark` query param on first load and a
`postMessage({ type: "telos:theme", theme })` when the operator toggles. Wire it
up with `reference/theme.js`:

1. Copy `theme.css` and `theme.js` into the app (e.g. `src/`).
2. Import the stylesheet once: `import "./theme.css";`
3. Call `initDashboardTheme()` once at startup (top of `src/main.jsx`, before
   render). It applies the initial mode and toggles `.dark` live on messages.
4. Style everything with `var(--token)` — never literal hex/oklch in components.

The result: the iframe matches the console in light mode, dark mode, and follows
the toggle live without a reload.

```
Tokens (from theme.css; values differ per mode, names are stable):
  --background        page background
  --foreground        primary text
  --card              card background
  --muted-foreground  labels / secondary text
  --border            card + divider borders
  --primary           primary accent / buttons
  --success           healthy / green
  --warning           warning / amber
  --destructive       error / red
  --radius            card radius (0.625rem; ~6px for small controls)

Typography (CSS vars --font-sans / --font-mono in theme.css):
  UI text / labels: "IBM Plex Sans"
  Values / IDs / code: "Geist Mono"
  Sizes: 0.8125rem (13px) body/values, 0.75rem (12px) labels,
         0.875rem (14px) section titles, 1.25rem (20px) metrics
  Load "IBM Plex Sans" and "Geist Mono" from a CDN (e.g. Google Fonts /
  Fontsource); they fall back to system fonts if unavailable.

Layout:
  Page background: var(--background)
  Cards: background var(--card), border 1px solid var(--border),
         border-radius var(--radius)
  Spacing scale: 4 / 8 / 12 / 16 / 24px — use only these steps.
  Card padding: 1rem–1.25rem. Gap between sections: 24px. Gap between
  tiles in a row: 16px.
```

Compose the page from the layout primitives in `reference/components.jsx` —
`Page` (centered canvas, 24px rhythm between sections) wrapping the whole
dashboard, and `Grid` (16px gap, auto-fit) for any row of tiles. Let those
primitives own the spacing rather than setting gaps by hand on each section.
The most common regression is sibling cards rendered flush against each other
(effectively `gap: 0`) so the page reads as one solid slab — that is exactly
what `Page`/`Grid` prevent. Never set a section or grid `gap` to `0`, and never
stack cards with no space between them.

## Quality bar — must not look AI-generated

This ships to a real operator and sits inside the Telos console. Hold it to the
same bar as the console itself. The goal is a dashboard that looks like a person
on the product team built it — restrained, consistent, and boring in the way
good internal tools are boring.

- **Match the console.** Neutral palette (light and dark), IBM Plex Sans for UI
  text, Geist Mono for values. No gradients, no emoji, no neon accents, no
  shadow on every element, no purple.
- **Calm spacing, never flush.** Compose with `Page`/`Grid` (see Design System)
  so there is always an even gap between cards — 24px between sections, 16px
  between tiles. Generous card padding (1–1.25rem), a centered max width
  (~760–1040px). Cards touching with no gap is the single most common tell.
- **One alignment grid.** Consistent left edge, one label-column width, steady
  vertical rhythm on the 4 / 8 / 12 / 16 / 24px scale.
- **Restrained hierarchy.** One page title, clear section titles, quiet labels.
  Sentence case for titles and labels (not Title Case or ALL CAPS headlines).
  Don't bold everything, don't center body text, keep to ~3 type sizes.
- **State is a dot, not a banner.** Show health/status as a small `StatusDot` +
  a sentence-case word via `StatusTile` ("Healthy", "Degraded"). Do not render
  giant lowercase colored monospace status words — that "status board" look is
  the strongest AI tell. Reserve color for genuine state (a red/amber dot or a
  small alert word), not decoration.
- **Numbers vs. words.** Monospace + tabular-nums is for real IDs, values, and
  counts only (`MetricCard`). Labels, titles, and prose are sans and sentence
  case. Never set a whole card in monospace.
- **Nothing overflows.** Every value wraps (`overflowWrap: "anywhere"`) or
  truncates with ellipsis; give its flex container `minWidth: 0`. Long DSNs,
  URLs, and cert fingerprints are the usual offenders. Test narrow.
- **Real states, not blank boxes.** Render `LoadingState` before the first fetch
  resolves and `EmptyState` when a section has no data. Never show an empty card
  or a raw `undefined` / `NaN`.
- **Buttons that work.** Every Show/Hide and Copy must function (see Sensitive
  Data). A button that no-ops is worse than no button.

### AI tells to avoid

These are the patterns that make a dashboard read as machine-generated. If you
catch yourself doing one, stop:

- Cards packed edge-to-edge with no gap (the whole page as one slab).
- Giant lowercase colored words ("healthy", "unhealthy") as headline "metrics".
- Everything in monospace, or every label in ALL CAPS.
- Color used as decoration (colored borders/backgrounds everywhere) instead of
  signalling real state.
- A status pill *and* a status dot *and* colored text all saying the same thing.
- Bold applied to whole rows; centered body text; five different font sizes.
- Redundant boilerplate copy ("This section displays…", "Below you can find…").

## Dashboard Sections

Adapt sections based on what the service actually has. Not all apply to every
service. Inspect what's deployed and include only relevant sections.

### Universal sections (always include):

1. **Header** — Service name, namespace, status dot, last-updated timestamp
2. **Health** — A service-level verdict: is the service reachable and actually
   serving? Use `HealthRow` per check (e.g. "Accepting connections", "HTTP 200 ·
   42ms", "Replication caught up"). Do **not** surface Kubernetes internals —
   no pod names, phases, restart counts, images, or node placement. The operator
   cares whether the service works, not how the cluster runs it. Derive the
   verdict from a real probe (a query, an HTTP check); you may use kubectl behind
   the scenes, but never render pod/replica objects.
3. **Networking** — Public handle / URL, ports, and the in-cluster DNS name the
   service uses. Keep it about how to reach the service, not cluster plumbing.
4. **Events** — Only if the service itself exposes meaningful events (audit log,
   job outcomes, service log). Use `EventRow`. Do **not** render raw Kubernetes
   events (BackOff, FailedScheduling, Unhealthy probe, …) — those are cluster
   internals. If the service has no such feed, omit this section.

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

- Use the `SecretValue` component from `reference/components.jsx` — do not
  hand-roll masking. It encodes a working reveal + copy.
- The API must return the **real** value to the authenticated frontend so reveal
  has something to show. Pass that real value to `SecretValue`; it masks it for
  display (`••••••••••`) and reveals on toggle. Masking the value server-side
  turns the reveal button into a no-op — a recurring bug; don't do that.
- Reveal (Show/Hide) and Copy must both actually work. The dashboard runs in an
  iframe, so `navigator.clipboard` may be blocked — `SecretValue`/`CopyValue`
  already fall back to a textarea + `execCommand`; keep that fallback.
- Copy copies the real value, never the mask.
- Never log or print passwords during the build.

## API Design

The Express API should have endpoints for each section:

```
GET /api/health     → { healthy: bool, checks: [{ name, ok, detail }] }
GET /api/connection → { dsn, host, port, user, password, ... }
GET /api/metrics    → { ... service-specific metrics ... }
GET /api/events     → { events: [...] }   # only if the service exposes them
```

`/api/health` returns a service-level verdict, **not** Kubernetes objects. Probe
the service the way a client would and report whether it is serving. You may use
kubectl behind the scenes, but never return pod/replica objects (names, phases,
restarts, images, nodes) or raw cluster events to the frontend.

```bash
# Health — probe the service itself, not the pods.
# PostgreSQL: can we connect and run a trivial query?
if PGPASSWORD=$PASS psql -h "$HOST" -U "$USER" -d "$DB" -tAc 'SELECT 1' >/dev/null 2>&1; then
  echo '{"name":"Database","ok":true,"detail":"Accepting connections"}'
else
  echo '{"name":"Database","ok":false,"detail":"Connection refused"}'
fi

# HTTP service: does the endpoint answer 200?
code=$(curl -s -o /dev/null -w '%{http_code}' "http://$HOST:$PORT/healthz")
[ "$code" = "200" ] && ok=true || ok=false
echo "{\"name\":\"API\",\"ok\":$ok,\"detail\":\"HTTP $code\"}"

# Networking — how to reach the service (ports + in-cluster DNS), not plumbing.
kubectl get svc "$SERVICE" -n "$NAMESPACE" -o json | jq '{
  dns: (.metadata.name + "." + .metadata.namespace + ".svc.cluster.local"),
  ports: [.spec.ports[] | {port: .port, protocol: .protocol}]
}'
```

For the Events section, read the service's own event/audit feed (e.g. a
`SELECT ... FROM audit_log`, an admin API, or `kubectl logs` parsed for the
service's structured events) — never `kubectl get events`.

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
npm install --save-dev esbuild
```

### 2. Write the API (server.js)

Express server that:
- Runs kubectl and service queries on each request
- Serves the built React frontend from `dist/`
- Listens on port 3000
- Is bundled before deployment, so the pod does not need `node_modules`

### 3. Write the React frontend (src/App.jsx)

- Copy `theme.css`, `theme.js`, and `components.jsx` from `reference/` into `src/`
- Import `./theme.css` once and call `initDashboardTheme()` (from `./theme.js`)
  at startup so the dashboard follows the console's light/dark mode
- Fetches from `/api/*` endpoints
- Auto-refreshes every 10 seconds
- Styles everything with `var(--token)` (see Design System) and uses the
  reference components from `components.jsx` — no hardcoded colors
- Wraps the whole dashboard in `Page` and puts tile rows in `Grid` so spacing is
  correct by construction (see Design System) — never stack cards flush
- Shows health via `HealthRow`/`StatusTile` (service-level), not pod internals
- Renders `LoadingState` before first data and `EmptyState` when a section is empty
- Adapts sections to the data available

### 4. Build and deploy

```bash
npm run build
npx esbuild server.js \
  --bundle \
  --minify \
  --platform=node \
  --target=node22 \
  --format=cjs \
  --outfile=server.bundle.cjs
tar -czf dist.tar.gz dist

# Package recursively built assets plus the bundled server. Do this before
# applying the Deployment so new pods can mount a complete app immediately.
kubectl create configmap dashboard-app \
  --from-file=server.bundle.cjs \
  --from-file=dist.tar.gz \
  -n "$NAMESPACE" \
  --dry-run=client \
  -o yaml | kubectl apply -n "$NAMESPACE" -f -

# Create a Deployment running the Express server
kubectl apply -n "$NAMESPACE" -f - <<EOF
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
        command:
        - sh
        - -c
        - |
          set -eu
          cp /bundle/server.bundle.cjs /app/server.bundle.cjs
          tar -xzf /bundle/dist.tar.gz -C /app
          exec node /app/server.bundle.cjs
        workingDir: /app
        ports:
        - containerPort: 3000
        volumeMounts:
        - name: bundle
          mountPath: /bundle
          readOnly: true
        - name: app
          mountPath: /app
      volumes:
      - name: bundle
        configMap:
          name: dashboard-app
      - name: app
        emptyDir: {}
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

kubectl rollout restart deployment/dashboard -n "$NAMESPACE"
kubectl rollout status deployment/dashboard -n "$NAMESPACE"
```

Do not mount raw `server.js` plus `dist/` directly from a ConfigMap: the pod will
not have `express` installed, and `--from-file=dist/` does not preserve Vite's
nested `dist/assets` tree. Bundle and minify the server, tar the built assets as
shown, and keep runtime dependencies minimal so the ConfigMap stays under the
Kubernetes object size limit. If the bundle is too large, remove dependencies or
rewrite the server with Node's built-in modules before deploying.

## Component Patterns

Use the reference implementations in `reference/components.jsx` rather than
hand-rolling — they encode the console styling plus the overflow, copy, and
reveal fixes. Available: `Page` and `Grid` (layout primitives that own the
spacing), `Card`, `SecretValue` (masked value with working reveal + copy),
`CopyValue` (copyable host / URL / DSN / handle), `StatusDot`, `StatusTile`
(dot + sentence-case status for the KPI row), `HealthRow` (service-level health
check), `MetricCard` (numeric metric), `EventRow` (application-level events),
`CertDownload`, `EmptyState`, `LoadingState`. Copy them into the app and compose
layout with `Page`/`Grid`; do not regress the `minWidth: 0` overflow handling or
the clipboard fallback.

## Verification

- `dashboard-route` receives a `hostname` from the route reconciler
- Dashboard loads at the allocated route URL
- Data refreshes every 10 seconds
- Health reflects whether the service is actually serving (probe result), and no
  Kubernetes internals are shown (no pod names/phases/restarts/images, no raw
  cluster events)
- Connection info matches actual service secrets
- Passwords masked by default, revealable
- All Telos styling applied (neutral theme matching the console in light and dark — correct fonts and colors)
- Service is named `dashboard` and the route ConfigMap has `type=dashboard`

### Self-QA — open the live dashboard and verify by interaction

Do not declare done from code review alone. Load the dashboard URL and check:

- [ ] Click every **Show** — the real secret appears (not still dots); **Hide** re-masks.
- [ ] Click every **Copy** — paste elsewhere and confirm the real value landed.
- [ ] No text overflows its box. Narrow the window to ~600px and recheck long
      DSNs, URLs, and fingerprints.
- [ ] Every card/section has a visible, even gap around it — no two cards touch,
      nothing collapses to a single slab. Check the top KPI row especially.
- [ ] No Kubernetes internals leaked: no pod names/phases/restarts/images, no
      raw cluster events. Health reads as a service-level verdict.
- [ ] Status reads as a dot + sentence-case word, not a giant lowercase colored
      monospace headline. Labels are sentence case; mono is only on real values.
- [ ] Each section shows a loading state on first paint and an empty state when
      data is absent — no blank cards, no `undefined` / `NaN`.
- [ ] It reads as part of the Telos console: correct fonts, one alignment grid,
      calm spacing. Nothing looks AI-generated.
- [ ] Load with `?theme=dark` and `?theme=light` — colors match the console in
      both. Then toggle the console's theme switch and confirm the dashboard
      follows live, in the same view, with no reload.
- [ ] The 10s refresh does not flicker or cause layout jump.
