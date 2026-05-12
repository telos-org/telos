---
name: k8s-troubleshoot
description: |
  Kubernetes troubleshooting and diagnosis. Use when fixing failures in
  a running system — pod crashes, service unreachable, restart loops,
  resource exhaustion. Loaded when liveness checks fail.
metadata:
  category: infrastructure
  author: telos
allowed-tools: Bash(kubectl:*)
---

# Kubernetes Troubleshooting

You are diagnosing failures in a Kubernetes namespace. Follow this
systematic approach to find and fix root causes.

## Triage Order

1. **Events** — what happened recently?
2. **Pod status** — are pods running?
3. **Logs** — what do containers say?
4. **Describe** — what does K8s think?
5. **Resources** — is anything constrained?

## Commands

```bash
# Recent events (most useful for diagnosing)
kubectl get events -n <namespace> --sort-by=.lastTimestamp | tail -20

# Pod overview
kubectl get pods -n <namespace> -o wide

# Pod details (scheduling, probes, volumes)
kubectl describe pod -n <namespace> <pod-name>

# Container logs (current)
kubectl logs -n <namespace> <pod-name> --tail=50

# Container logs (previous crash)
kubectl logs -n <namespace> <pod-name> --previous --tail=50

# Resource usage
kubectl top pods -n <namespace>

# Service endpoints (is the service routing?)
kubectl get endpoints -n <namespace>
```

## Common Failure Patterns

### ImagePullBackOff
- Wrong image name/tag
- Private registry without credentials
- Fix: check image name, add imagePullSecret

### CrashLoopBackOff
- Application crashes on startup
- The crash reason is almost always in the logs — look there first
- For multi-container pods, check each container: `kubectl logs <pod> -c <container> --previous`
- For init containers specifically: `kubectl logs <pod> -c <init-container>` (init containers don't need `--previous` if still crashing)
- Common causes: wrong entrypoint/command, missing binary in image, bad config, dependency not ready
- Fix: read the logs, identify the exact error, then fix the manifest

### Pending Pod
- Insufficient resources or no matching node
- Fix: check events, reduce resource requests, or add nodes

### Service Not Reachable
- Selector doesn't match pod labels
- Wrong port mapping
- Fix: compare `kubectl get svc` ports with `kubectl get pods --show-labels`

### Readiness Probe Failing
- Service not ready yet (increase `initialDelaySeconds`)
- Wrong probe command/path
- Fix: exec into pod and test the probe command manually

## Recovery Patterns

```bash
# Restart a deployment (rolling)
kubectl rollout restart deployment/<name> -n <namespace>

# Scale to 0 and back (hard reset)
kubectl scale deployment/<name> -n <namespace> --replicas=0
kubectl scale deployment/<name> -n <namespace> --replicas=1

# Delete and let controller recreate
kubectl delete pod <pod-name> -n <namespace>

# Force delete stuck pod
kubectl delete pod <pod-name> -n <namespace> --grace-period=0 --force
```
