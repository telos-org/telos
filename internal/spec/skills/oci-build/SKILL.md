---
name: oci-build
description: |
  Build and deploy services in Telos clusters.
  Build locally (node/python available), deploy with base images
  from the cluster. Mount code from the shared workspace PVC.
  No Docker daemon, no Kaniko, no ConfigMap hacks.
metadata:
  category: build
  author: telos
allowed-tools: Bash(kubectl:*,npm:*,node:*,python3:*,pip:*)
---

# Building and Deploying Services

You are in a Telos workspace. Node.js, Python, and all build tools are available.

**Build locally in the workspace. Deploy with base images. Mount code from the workspace PVC.**

Do NOT use Docker, Kaniko, or store source in ConfigMaps.

## Pattern: Next.js Service

```bash
# 1. Build in workspace
cd /workspace/output/services/my-frontend
npm install
npm run build

# 2. Prepare standalone output
mkdir -p dist
cp -r .next/standalone/* dist/
cp -r .next/static dist/.next/static
mkdir -p dist/public
cp -r public/* dist/public/ 2>/dev/null || true
```

Deploy with workspace PVC mount:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: my-frontend
spec:
  replicas: 1
  selector:
    matchLabels:
      app: my-frontend
  template:
    metadata:
      labels:
        app: my-frontend
    spec:
      containers:
        - name: app
          image: node:22-slim
          command: ["node", "server.js"]
          workingDir: /app
          ports:
            - containerPort: 3000
          env:
            - name: NODE_ENV
              value: production
            - name: PORT
              value: "3000"
            - name: HOSTNAME
              value: "0.0.0.0"
          volumeMounts:
            - name: workspace
              mountPath: /app
              subPath: services/my-frontend/dist
      volumes:
        - name: workspace
          persistentVolumeClaim:
            claimName: agent-workspace
```

### next.config.js must include:

```js
const nextConfig = {
  output: 'standalone',  // REQUIRED — produces self-contained server.js
  async rewrites() {
    return [{
      source: '/api/:path*',
      destination: `${process.env.API_URL || 'http://localhost:8000'}/api/:path*`,
    }];
  },
};
```

## Pattern: Django / Python Service

```bash
# 1. Write app in workspace
cd /workspace/output/services/my-api

# 2. Deploy with init container for deps
```

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: my-api
spec:
  replicas: 1
  selector:
    matchLabels:
      app: my-api
  template:
    metadata:
      labels:
        app: my-api
    spec:
      initContainers:
        - name: install-deps
          image: python:3.12-slim
          command: ["sh", "-c", "pip install -r /app/requirements.txt --target /deps"]
          volumeMounts:
            - name: workspace
              mountPath: /app
              subPath: services/my-api
            - name: deps
              mountPath: /deps
      containers:
        - name: app
          image: python:3.12-slim
          command: ["gunicorn", "myapp.wsgi:application", "-b", "0.0.0.0:8000"]
          workingDir: /app
          env:
            - name: PYTHONPATH
              value: /app:/deps
          ports:
            - containerPort: 8000
          volumeMounts:
            - name: workspace
              mountPath: /app
              subPath: services/my-api
            - name: deps
              mountPath: /deps
      volumes:
        - name: workspace
          persistentVolumeClaim:
            claimName: agent-workspace
        - name: deps
          emptyDir: {}
```

## Pattern: PostgreSQL / Stateful Service

Use the upstream image directly — no build needed:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: postgres
spec:
  replicas: 1
  selector:
    matchLabels:
      app: postgres
  template:
    spec:
      containers:
        - name: postgres
          image: postgres:16-alpine
          ports:
            - containerPort: 5432
          env:
            - name: POSTGRES_DB
              value: mydb
            - name: POSTGRES_USER
              valueFrom:
                secretKeyRef:
                  name: postgres-credentials
                  key: POSTGRES_USER
            - name: POSTGRES_PASSWORD
              valueFrom:
                secretKeyRef:
                  name: postgres-credentials
                  key: POSTGRES_PASSWORD
          volumeMounts:
            - name: data
              mountPath: /var/lib/postgresql/data
      volumes:
        - name: data
          persistentVolumeClaim:
            claimName: postgres-data
```

## Alternative: Build OCI Images with Bazel rules_oci

The workspace has a pre-configured MODULE.bazel with rules_oci 2.3.0 and tar.bzl 0.9.0.
Use this when you need a self-contained image (no workspace PVC mount).

**Important: use `tar()` from `@tar.bzl`, NOT `pkg_tar()` from `@rules_pkg`.**

```python
# services/my-api/BUILD.bazel
load("@tar.bzl", "tar")
load("@rules_oci//oci:defs.bzl", "oci_image", "oci_load")

tar(
    name = "app_layer",
    srcs = glob(["**/*.py", "**/*.txt"]),
)

oci_image(
    name = "image",
    base = "@python3_slim",
    tars = [":app_layer"],
    entrypoint = ["python3", "-m", "gunicorn", "myapp.wsgi:application", "-b", "0.0.0.0:8000"],
    env = {"PYTHONPATH": "/app"},
)

oci_load(
    name = "load",
    image = ":image",
    repo_tags = ["my-api:latest"],
)
```

Build and load:
```bash
bazel build //services/my-api:load
# The oci_load target loads the image into the local container runtime
```

## Key Rules

1. **Build locally** — npm, node, python3, pip are all in your agent image
2. **Deploy with base images** — `node:22-slim`, `python:3.12-slim`, `postgres:16-alpine` are pre-loaded in the cluster
3. **Mount from workspace PVC** — use `subPath` to mount just your service directory
4. **Never use ConfigMaps for source code** — the workspace PVC is the source of truth
5. **Always use `output: 'standalone'`** for Next.js
6. **Use init containers** for pip install / migrations / setup
7. **Cross-namespace DNS**: `<service>.<namespace>.svc.cluster.local`
8. **Use `tar()` from `@tar.bzl`** — NOT `pkg_tar()` from `@rules_pkg` (ARM64 compat)
