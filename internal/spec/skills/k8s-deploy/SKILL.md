---
name: k8s-deploy
description: |
  Kubernetes deployment patterns for greenfield builds. Use when building
  systems from scratch in an empty namespace — deployments, services, PVCs,
  health probes, RBAC. Always loaded for k8s-based environments.
metadata:
  category: infrastructure
  author: telos
allowed-tools: Bash(kubectl:*) Bash(helm:*)
---

# Kubernetes Deployment

You are deploying a system into a Kubernetes namespace. You have full kubectl
access. Follow these patterns for reliable deployments.

## GitOps: Workspace is Source of Truth

Every K8s resource you create MUST exist as a YAML manifest in your workspace.
The workspace is the declarative source of truth.

**Path convention:**
- Manifests: `services/<service-name>/k8s/`
- Tests: `services/<service-name>/tests/`

**Workflow — always write-then-apply:**

```bash
# 1. Write the manifest
cat > services/myservice/k8s/deployment.yaml << 'EOF'
apiVersion: apps/v1
kind: Deployment
...
EOF

# 2. Apply it
kubectl apply -f services/myservice/k8s/deployment.yaml
```

**Never use imperative commands that skip the workspace:**
- `kubectl create deployment ...` — NO
- `kubectl run ...` — NO
- `kubectl create secret generic ... --from-literal=...` — NO

If you must generate YAML, capture it:
```bash
kubectl create secret generic my-secret --from-literal=key=val \
  --dry-run=client -o yaml > services/myservice/k8s/secret.yaml
kubectl apply -f services/myservice/k8s/secret.yaml
```

**Replay test:** `kubectl apply -f services/<name>/k8s/` in a fresh namespace
must recreate the entire deployment. If it can't, your workspace is broken.

**Commit after each milestone:** `git add -A && git commit -m '<description>'`

## Construction Order

1. **Write all manifests** to `services/<name>/k8s/`
2. **Apply PersistentVolumeClaims** (storage before workloads)
3. **Apply ConfigMaps/Secrets** (configuration before workloads)
4. **Apply Deployment/StatefulSet** (the workload)
5. **Apply Service** (expose the workload)
6. **Wait for rollout** (`kubectl rollout status`)
7. **Verify liveness** (protocol-specific check)
8. **Commit** (`git add -A && git commit -m '<description>'`)

## Deployment Best Practices

- Always set `readinessProbe` and `livenessProbe`
- Always set resource `requests` and `limits`
- Use `imagePullPolicy: IfNotPresent` for tagged images
- Use labels consistently: `app: <name>`, `version: <ver>`
- Use ClusterIP for in-cluster services; publish externally through Telos handles

## Health Probes

```yaml
readinessProbe:
  exec:
    command: ["<protocol-check>"]
  initialDelaySeconds: 5
  periodSeconds: 5
livenessProbe:
  exec:
    command: ["<protocol-check>"]
  initialDelaySeconds: 15
  periodSeconds: 10
```

Protocol-specific checks:
- **RESP**: `redis-cli ping`
- **SQL**: `pg_isready -U postgres`
- **HTTP**: `curl -sf http://localhost:<port>/health`

## Waiting for Readiness

```bash
# Wait for deployment
kubectl rollout status deployment/<name> -n <namespace> --timeout=120s

# Check pod status
kubectl get pods -n <namespace> -l app=<name>

# Describe pod on failure
kubectl describe pod -n <namespace> -l app=<name>

# Check events
kubectl get events -n <namespace> --sort-by=.lastTimestamp
```

## Debugging Failures

See [references/troubleshooting.md](references/troubleshooting.md) for common
failure patterns and fixes.

## Writing Bazel Test Targets

Write test targets that verify your deployment:

```python
# test_deployment.py
import subprocess
import json

def test_pods_ready():
    """All pods in namespace should be ready."""
    result = subprocess.run(
        ["kubectl", "get", "pods", "-n", "<namespace>",
         "-o", "json"],
        capture_output=True, text=True,
    )
    pods = json.loads(result.stdout)
    for pod in pods["items"]:
        conditions = {c["type"]: c["status"]
                      for c in pod["status"].get("conditions", [])}
        assert conditions.get("Ready") == "True", \
            f"Pod {pod['metadata']['name']} not ready"

def test_no_restart_loops():
    """No container should have >= 3 restarts."""
    result = subprocess.run(
        ["kubectl", "get", "pods", "-n", "<namespace>",
         "-o", "json"],
        capture_output=True, text=True,
    )
    pods = json.loads(result.stdout)
    for pod in pods["items"]:
        for cs in pod["status"].get("containerStatuses", []):
            assert cs["restartCount"] < 3, \
                f"Container {cs['name']} has {cs['restartCount']} restarts"
```
