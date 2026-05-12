---
name: k8s-fault-runtime
description: |
  Runtime fault injection — OOMKill, probe misconfiguration, crash loops,
  container command failures. Pods start but fail during execution.
  Easy-medium difficulty: container logs and pod status provide direct clues.
metadata:
  category: fault-injection
  role: environment-generator
  difficulty: easy
  source: cloud-opsbench
  author: telos
allowed-tools: Bash(kubectl:*)
---

# Runtime Fault Injection

Faults in this category cause containers to fail after scheduling. Pods
are placed on nodes but crash, get OOMKilled, or fail health checks.
These produce the most explicit symptoms — container logs, restart counts,
and pod status directly indicate the problem.

## Operators

### OOMKilled

Set memory limits too low for the application's actual usage. The container
starts, allocates memory, and gets killed by the OOM killer.

```bash
fault_oom_kill() {
  local deploy=$1 ns=$2
  local memory_limit=${3:-"16Mi"}  # too low for most applications

  kubectl patch deployment "$deploy" -n "$ns" --type=json -p "[
    {\"op\":\"replace\",\"path\":\"/spec/template/spec/containers/0/resources/limits/memory\",
     \"value\":\"$memory_limit\"},
    {\"op\":\"replace\",\"path\":\"/spec/template/spec/containers/0/resources/requests/memory\",
     \"value\":\"$memory_limit\"}
  ]"
}

verify_oom() {
  local deploy=$1 ns=$2
  kubectl get pods -n "$ns" -l app="$deploy" -o jsonpath='{.items[*].status.containerStatuses[*].lastState.terminated.reason}' | \
    grep -q "OOMKilled" && echo "OK: OOMKilled observed" || echo "WAIT: OOM not yet triggered"
}
```

### LivenessProbeIncorrectPort

Change the liveness probe port to one the application doesn't listen on.
The container starts and runs, but kubelet's probe fails. After
failureThreshold attempts, the container is killed and restarted — CrashLoopBackOff.

```bash
fault_liveness_wrong_port() {
  local deploy=$1 ns=$2
  local wrong_port=${3:-"9999"}

  kubectl patch deployment "$deploy" -n "$ns" --type=json -p "[
    {\"op\":\"replace\",\"path\":\"/spec/template/spec/containers/0/livenessProbe/tcpSocket/port\",
     \"value\":$wrong_port}
  ]"
}

# Alternative: if probe is httpGet, change the port there
fault_liveness_wrong_http_port() {
  local deploy=$1 ns=$2
  local wrong_port=${3:-"9999"}

  kubectl patch deployment "$deploy" -n "$ns" --type=json -p "[
    {\"op\":\"replace\",\"path\":\"/spec/template/spec/containers/0/livenessProbe/httpGet/port\",
     \"value\":$wrong_port}
  ]"
}
```

### LivenessProbeIncorrectProtocol

Change probe type — e.g., switch from tcpSocket to httpGet on a service
that doesn't serve HTTP. Probe always fails.

```bash
fault_liveness_wrong_protocol() {
  local deploy=$1 ns=$2
  local port=${3:-"5432"}  # e.g., postgres port — not HTTP

  kubectl patch deployment "$deploy" -n "$ns" --type=json -p "[
    {\"op\":\"remove\",\"path\":\"/spec/template/spec/containers/0/livenessProbe/tcpSocket\"},
    {\"op\":\"add\",\"path\":\"/spec/template/spec/containers/0/livenessProbe/httpGet\",
     \"value\":{\"path\":\"/healthz\",\"port\":$port}}
  ]"
}
```

### LivenessProbeIncorrectTiming

Set initialDelaySeconds too low for applications with slow startup. The
probe fires before the app is ready, kills it, restarts, same thing — endless
CrashLoopBackOff with a healthy application.

```bash
fault_liveness_timing() {
  local deploy=$1 ns=$2

  kubectl patch deployment "$deploy" -n "$ns" --type=json -p "[
    {\"op\":\"replace\",\"path\":\"/spec/template/spec/containers/0/livenessProbe/initialDelaySeconds\",
     \"value\":0},
    {\"op\":\"replace\",\"path\":\"/spec/template/spec/containers/0/livenessProbe/timeoutSeconds\",
     \"value\":1},
    {\"op\":\"replace\",\"path\":\"/spec/template/spec/containers/0/livenessProbe/failureThreshold\",
     \"value\":1}
  ]"
}
```

### ReadinessProbeIncorrectPort / Protocol

Same as liveness but for readiness. Pod starts and stays Running, but
readiness fails — endpoints are never populated, Service routes no traffic.
More subtle than liveness failures because the pod doesn't restart.

```bash
fault_readiness_wrong_port() {
  local deploy=$1 ns=$2
  local wrong_port=${3:-"9999"}

  kubectl patch deployment "$deploy" -n "$ns" --type=json -p "[
    {\"op\":\"replace\",\"path\":\"/spec/template/spec/containers/0/readinessProbe/tcpSocket/port\",
     \"value\":$wrong_port}
  ]"
}

verify_readiness_failing() {
  local deploy=$1 ns=$2
  # pod is Running but not Ready — 0/1 READY
  kubectl get pods -n "$ns" -l app="$deploy" --no-headers | \
    grep -q "0/1" && echo "OK: pod not ready" || echo "FAIL: pod is ready"
}
```

### CrashLoopBackOff via Bad Command

Override the container command with something that exits immediately.
Clean CrashLoopBackOff — logs show the error from the bad command.

```bash
fault_bad_command() {
  local deploy=$1 ns=$2
  local bad_cmd=${3:-"exit 1"}

  kubectl patch deployment "$deploy" -n "$ns" --type=json -p "[
    {\"op\":\"replace\",\"path\":\"/spec/template/spec/containers/0/command\",
     \"value\":[\"sh\",\"-c\",\"$bad_cmd\"]}
  ]"
}

# Subtler variant: command that works but produces wrong behavior
fault_wrong_config_flag() {
  local deploy=$1 ns=$2

  # Add an invalid flag or config file path
  kubectl patch deployment "$deploy" -n "$ns" --type=json -p "[
    {\"op\":\"add\",\"path\":\"/spec/template/spec/containers/0/args/-\",
     \"value\":\"--config=/etc/nonexistent/config.yaml\"}
  ]"
}
```

### Environment Variable Corruption

Change a critical environment variable to a wrong value. Application starts
but fails to connect to dependencies, or uses wrong credentials.

```bash
fault_wrong_env() {
  local deploy=$1 ns=$2
  local env_name=$3
  local wrong_value=$4

  kubectl set env deployment/"$deploy" -n "$ns" "$env_name=$wrong_value"
}

# Common patterns:
# fault_wrong_env "myapp" "$NS" "DATABASE_HOST" "postgres-wrong-hostname"
# fault_wrong_env "myapp" "$NS" "DATABASE_PORT" "9999"
# fault_wrong_env "myapp" "$NS" "REDIS_PASSWORD" "wrong-password"
```

## Composition Patterns

```bash
# OOM + bad probe timing — agent sees CrashLoopBackOff but root cause is OOM,
# and aggressive probe timing masks the real error
fault_oom_kill "$DEPLOY" "$NS" "32Mi"
fault_liveness_timing "$DEPLOY" "$NS"

# Readiness failure + wrong env — service is "running" but broken in two ways
fault_readiness_wrong_port "$DEPLOY" "$NS" "8888"
fault_wrong_env "$DEPLOY" "$NS" "DATABASE_HOST" "wrong-host"

# Cascading: primary DB crashes (OOM) → dependent app can't connect → readiness fails
fault_oom_kill "postgres" "$NS" "32Mi"
# app's readiness probe checks DB connectivity — fails because DB is down
```

## Symptom Characteristics

Easy-medium — symptoms are explicit:
- `CrashLoopBackOff` status visible in `kubectl get pods`
- `OOMKilled` reason in `kubectl describe pod`
- Container logs show application errors
- Restart count increments visibly
- Events show `Killing`, `BackOff`, `Unhealthy`

Readiness faults are harder (medium) because:
- Pod shows `Running` but `0/1 Ready`
- No restarts — looks like the pod is "fine" at first glance
- Traffic silently fails (service has no endpoints)
