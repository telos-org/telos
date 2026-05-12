---
name: k8s-fault-infra
description: |
  Infrastructure and performance fault injection — node-level disruptions,
  network chaos, CPU stress, component failures. Hard difficulty: symptoms
  are distributed across multiple layers with no single root cause indicator.
  Requires chaos tooling (ChaosBlade/tc/stress-ng) beyond kubectl.
metadata:
  category: fault-injection
  role: environment-generator
  difficulty: hard
  source: cloud-opsbench
  author: telos
allowed-tools: Bash(kubectl:*) Bash(chaosblade:*) Bash(tc:*) Bash(stress-ng:*)
---

# Infrastructure & Performance Fault Injection

Faults in this category operate at the node and network infrastructure level.
These produce the most implicit symptoms — application latency, intermittent
failures, cascading errors across services. Diagnosis requires correlating
metrics, logs, and system state across multiple layers.

Some operators require chaos tooling (ChaosBlade, tc, stress-ng) beyond
standard kubectl. Hosted environments should prefer pod-scoped or
DaemonSet-based injections over direct node shell access.

## Operators

### PodCPUOverload

Inject a CPU stress workload that starves the target application of CPU.
Application becomes slow, health checks may time out, requests queue up.

```bash
# Using stress-ng inside the target container
fault_cpu_stress_in_pod() {
  local pod=$1 ns=$2
  local cpu_workers=${3:-"4"}
  local duration=${4:-"300"}  # seconds

  kubectl exec "$pod" -n "$ns" -- sh -c \
    "stress-ng --cpu $cpu_workers --timeout ${duration}s &"
}

# Using resource limits — more deterministic
fault_cpu_throttle() {
  local deploy=$1 ns=$2
  local cpu_limit=${3:-"50m"}  # 5% of a core

  kubectl patch deployment "$deploy" -n "$ns" --type=json -p "[
    {\"op\":\"replace\",\"path\":\"/spec/template/spec/containers/0/resources/limits/cpu\",
     \"value\":\"$cpu_limit\"}
  ]"
}

verify_cpu_throttle() {
  local deploy=$1 ns=$2
  local pod=$(kubectl get pods -n "$ns" -l app="$deploy" -o jsonpath='{.items[0].metadata.name}')
  # Check if pod is being throttled
  kubectl exec "$pod" -n "$ns" -- cat /sys/fs/cgroup/cpu/cpu.stat 2>/dev/null | \
    grep throttled || echo "cgroup stats not available"
}
```

### PodNetworkDelay

Inject network latency into a pod's traffic using tc (traffic control).
Application works but responses are slow. Connection timeouts in dependent
services.

```bash
fault_network_delay_sidecar() {
  local deploy=$1 ns=$2
  local delay=${3:-"500ms"}

  kubectl patch deployment "$deploy" -n "$ns" --type=json -p "[
    {\"op\":\"add\",\"path\":\"/spec/template/spec/containers/-\",\"value\":{
      \"name\":\"network-chaos\",
      \"image\":\"alpine:3.19\",
      \"command\":[\"sh\",\"-c\",\"apk add iproute2 && tc qdisc add dev eth0 root netem delay $delay && sleep infinity\"],
      \"securityContext\":{\"capabilities\":{\"add\":[\"NET_ADMIN\"]}}
    }}
  ]"
}
```

### NodeNetworkPacketLoss

Inject packet loss for a target workload. Very hard to diagnose — some
requests succeed, some fail, with no clear pattern.

```bash
fault_packet_loss() {
  local deploy=$1 ns=$2
  local loss=${3:-"30"}  # percent

  kubectl patch deployment "$deploy" -n "$ns" --type=json -p "[
    {\"op\":\"add\",\"path\":\"/spec/template/spec/containers/-\",\"value\":{
      \"name\":\"packet-loss-chaos\",
      \"image\":\"alpine:3.19\",
      \"command\":[\"sh\",\"-c\",\"apk add iproute2 && tc qdisc add dev eth0 root netem loss ${loss}% && sleep infinity\"],
      \"securityContext\":{\"capabilities\":{\"add\":[\"NET_ADMIN\"]}}
    }}
  ]"
}
```

### KubeletUnavailable

Simulate kubelet unavailability by preventing new work from landing on a
node and forcing rescheduling pressure through pod eviction. Directly
stopping kubelet requires out-of-band node authority and is not a normal
hosted controller move.

```bash
fault_node_unschedulable() {
  local node=${1:-$(kubectl get nodes -o jsonpath='{.items[0].metadata.name}')}
  kubectl cordon "$node"
}

verify_node_not_ready() {
  local node=${1:-$(kubectl get nodes -o jsonpath='{.items[0].metadata.name}')}
  kubectl get node "$node"
}
```

### ContainerdUnavailable

Simulate container runtime disruption by evicting or deleting pods from a
selected workload so controllers must recreate them under pressure. Direct
runtime shutdown requires node authority outside the hosted controller
surface.

```bash
fault_runtime_disruption() {
  local label=$1 ns=$2
  kubectl delete pod -n "$ns" -l "$label" --grace-period=0 --force
}
```

### KubeProxyUnavailable

Simulate kube-proxy or service routing disruption by changing Service
selectors so endpoints disappear without deleting the workload.

```bash
fault_service_selector_miss() {
  local service=$1 ns=$2
  kubectl patch service "$service" -n "$ns" --type=merge \
    -p '{"spec":{"selector":{"telos-fault":"no-matching-pods"}}}'
}
```

### Disk Pressure

Fill up a node's disk to trigger DiskPressure condition. Node starts
evicting pods. New pods with ephemeral storage requests can't schedule.

```bash
fault_disk_pressure() {
  local pod=$1 ns=$2
  local size=${3:-"1G"}

  # Fill disk inside a pod's writable layer
  kubectl exec "$pod" -n "$ns" -- sh -c \
    "dd if=/dev/zero of=/tmp/diskfill bs=1M count=1024" 2>/dev/null
}

# Safer: fill an emptyDir volume
fault_disk_fill_emptydir() {
  local deploy=$1 ns=$2
  local mount_path=${3:-"/tmp/data"}

  # First add an emptyDir with a size limit, then fill it
  kubectl patch deployment "$deploy" -n "$ns" --type=json -p "[
    {\"op\":\"add\",\"path\":\"/spec/template/spec/volumes/-\",\"value\":{
      \"name\":\"fill-volume\",
      \"emptyDir\":{\"sizeLimit\":\"100Mi\"}
    }},
    {\"op\":\"add\",\"path\":\"/spec/template/spec/containers/0/volumeMounts/-\",\"value\":{
      \"name\":\"fill-volume\",
      \"mountPath\":\"$mount_path\"
    }}
  ]"
  # Pod will be evicted when emptyDir exceeds sizeLimit
}
```

### Clock Skew

Adjust the system clock inside a container. TLS certificate validation fails,
token expiry checks break, cron jobs fire at wrong times.

```bash
fault_clock_skew() {
  local pod=$1 ns=$2
  local offset=${3:-"+2 days"}

  kubectl exec "$pod" -n "$ns" -- sh -c \
    "date -s \"\$(date -d '$offset')\"" 2>/dev/null || \
  kubectl exec "$pod" -n "$ns" -- sh -c \
    "apk add --no-cache tzdata && date -s \"\$(date -d '$offset')\"" 2>/dev/null || \
    echo "WARN: cannot change date (requires SYS_TIME capability)"
}
```

## Composition Patterns

```bash
# Network delay + CPU throttle — cascading slowness
fault_cpu_throttle "$DEPLOY" "$NS" "50m"
fault_network_delay_sidecar "$DEPLOY" "$NS" "200ms"

# Kubelet stop + disk pressure — node-level compound failure
fault_kubelet_stop
fault_disk_pressure "$POD" "$NS"

# Packet loss + clock skew — intermittent failures + TLS errors
fault_packet_loss "$POD" "$NS" "20"
fault_clock_skew "$POD" "$NS" "+1 year"  # certificates expire

# Cascading infrastructure: kube-proxy down + network delay
# Services slowly stop working as iptables rules go stale
fault_kube_proxy_stop
fault_network_delay_sidecar "app" "$NS" "300ms"
```

## Symptom Characteristics

Hard difficulty — symptoms are distributed and implicit:
- Latency increase without clear cause (no error, just slow)
- Intermittent failures (packet loss: some requests work, some don't)
- Node-level: pods move to Unknown, no logs available from the node
- Cascading: one infrastructure fault causes multiple service failures
- Metrics are the primary diagnostic tool (CPU usage, network latency)
- No single `kubectl describe` reveals the root cause
- Agent must correlate: metrics + node status + multiple pod logs + events

## Toolchain Requirements

Some operators require tools beyond kubectl:
- **tc** (iproute2): network delay, packet loss
- **stress-ng**: CPU/memory stress injection
- **nsenter**: entering container network namespaces

For hosted runs, node-level faults should be represented through Kubernetes
objects, privileged DaemonSets, or explicit chaos tooling with declared
authority. Do not assume direct node shell access.
