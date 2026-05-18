---
name: k8s-fault-scheduling
description: |
  Scheduling fault injection — node affinity mismatches, taint/toleration
  conflicts, PV binding failures, resource exhaustion. Pods exist but cannot
  be placed. Medium difficulty: events provide clues but root cause requires
  understanding scheduler constraints.
metadata:
  category: fault-injection
  role: environment-generator
  difficulty: medium
  source: cloud-opsbench
  author: telos
allowed-tools: Bash(kubectl:*)
---

# Scheduling Fault Injection

Faults in this category prevent the Kubernetes scheduler from placing pods
on nodes. Pods are created but stay Pending. The diagnosis path goes through
events → scheduler constraints → node state.

## Operators

### NodeCordoned

Cordon a node so no new pods can be scheduled on it. Existing pods remain
running. In a single-node cloud environment, this prevents all new scheduling.

```bash
fault_cordon_node() {
  local node=${1:-$(kubectl get nodes -o jsonpath='{.items[0].metadata.name}')}
  kubectl cordon "$node"
}

verify_cordon() {
  local node=${1:-$(kubectl get nodes -o jsonpath='{.items[0].metadata.name}')}
  kubectl get node "$node" -o jsonpath='{.spec.unschedulable}' | grep -q "true" && \
    echo "OK: node cordoned" || echo "FAIL: node not cordoned"
}
```

### NodeAffinityMismatch

Patch a deployment to require a node label that doesn't exist. The pod
is created but the scheduler can't find a matching node.

```bash
fault_node_affinity() {
  local deploy=$1 ns=$2
  local fake_label_key=${3:-"topology.kubernetes.io/zone"}
  local fake_label_value=${4:-"us-east-99z"}

  kubectl patch deployment "$deploy" -n "$ns" --type=json -p "[
    {\"op\":\"add\",\"path\":\"/spec/template/spec/affinity\",\"value\":{
      \"nodeAffinity\":{
        \"requiredDuringSchedulingIgnoredDuringExecution\":{
          \"nodeSelectorTerms\":[{
            \"matchExpressions\":[{
              \"key\":\"$fake_label_key\",
              \"operator\":\"In\",
              \"values\":[\"$fake_label_value\"]
            }]
          }]
        }
      }
    }}
  ]"
}

verify_node_affinity() {
  local deploy=$1 ns=$2
  # pods should be Pending with FailedScheduling event
  kubectl get pods -n "$ns" -l app="$deploy" -o jsonpath='{.items[*].status.phase}' | \
    grep -q "Pending" && echo "OK: pods pending (affinity mismatch)" || echo "FAIL: pods not pending"
}
```

### NodeSelectorMismatch

Simpler than affinity — add a nodeSelector for a nonexistent label.

```bash
fault_node_selector() {
  local deploy=$1 ns=$2
  local label_key=${3:-"node-role.kubernetes.io/gpu"}
  local label_value=${4:-"true"}

  kubectl patch deployment "$deploy" -n "$ns" --type=json -p "[
    {\"op\":\"add\",\"path\":\"/spec/template/spec/nodeSelector\",\"value\":{
      \"$label_key\":\"$label_value\"
    }}
  ]"
}
```

### TaintTolerationMismatch

Add a taint to the node that existing pod tolerations don't match.
New pods won't schedule; existing pods may be evicted depending on
the taint effect.

```bash
fault_taint_node() {
  local node=${1:-$(kubectl get nodes -o jsonpath='{.items[0].metadata.name}')}
  local taint_key=${2:-"dedicated"}
  local taint_value=${3:-"special-workload"}
  local effect=${4:-"NoSchedule"}  # NoSchedule | PreferNoSchedule | NoExecute

  kubectl taint nodes "$node" "$taint_key=$taint_value:$effect" --overwrite
}

verify_taint() {
  local node=${1:-$(kubectl get nodes -o jsonpath='{.items[0].metadata.name}')}
  local taint_key=${2:-"dedicated"}
  kubectl get node "$node" -o jsonpath='{.spec.taints[*].key}' | grep -q "$taint_key" && \
    echo "OK: taint applied" || echo "FAIL: taint not found"
}

# NoExecute variant — evicts existing pods too
fault_taint_evict() {
  local node=${1:-$(kubectl get nodes -o jsonpath='{.items[0].metadata.name}')}
  local taint_key=${2:-"maintenance"}
  kubectl taint nodes "$node" "$taint_key=true:NoExecute" --overwrite
}
```

### PodAntiAffinityConflict

Add pod anti-affinity that conflicts with existing pods on the same node.
In a single-node cluster, only one replica can run.

```bash
fault_anti_affinity() {
  local deploy=$1 ns=$2
  local label_key=${3:-"app"}
  local label_value=${4:-$deploy}

  kubectl patch deployment "$deploy" -n "$ns" --type=json -p "[
    {\"op\":\"add\",\"path\":\"/spec/template/spec/affinity\",\"value\":{
      \"podAntiAffinity\":{
        \"requiredDuringSchedulingIgnoredDuringExecution\":[{
          \"labelSelector\":{
            \"matchExpressions\":[{
              \"key\":\"$label_key\",
              \"operator\":\"In\",
              \"values\":[\"$label_value\"]
            }]
          },
          \"topologyKey\":\"kubernetes.io/hostname\"
        }]
      }
    }}
  ]"
}
```

### InsufficientNodeCPU / InsufficientNodeMemory

Set resource requests higher than node capacity. The scheduler rejects
placement due to insufficient resources.

```bash
fault_excessive_cpu_request() {
  local deploy=$1 ns=$2
  local cpu=${3:-"64"}  # 64 cores — no cloud node has this

  kubectl patch deployment "$deploy" -n "$ns" --type=json -p "[
    {\"op\":\"replace\",\"path\":\"/spec/template/spec/containers/0/resources/requests/cpu\",
     \"value\":\"$cpu\"}
  ]"
}

fault_excessive_memory_request() {
  local deploy=$1 ns=$2
  local memory=${3:-"256Gi"}  # 256GB should exceed ordinary cloud nodes

  kubectl patch deployment "$deploy" -n "$ns" --type=json -p "[
    {\"op\":\"replace\",\"path\":\"/spec/template/spec/containers/0/resources/requests/memory\",
     \"value\":\"$memory\"}
  ]"
}
```

### PVCBindingFaults

PVC fails to bind because no matching PV exists, or the PV is already
bound to another claim, or the access mode / capacity doesn't match.

```bash
# Wrong storage class — PVC references a class that doesn't exist
fault_pvc_wrong_class() {
  local pvc=$1 ns=$2
  local wrong_class=${3:-"premium-ssd-nonexistent"}

  kubectl patch pvc "$pvc" -n "$ns" --type=json -p "[
    {\"op\":\"replace\",\"path\":\"/spec/storageClassName\",\"value\":\"$wrong_class\"}
  ]"
}

# PVC capacity mismatch — request more than any PV can provide
fault_pvc_excessive_capacity() {
  local ns=$1
  local pvc_name=${2:-"data-claim"}

  kubectl apply -n "$ns" -f - <<EOF
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: $pvc_name
  namespace: $ns
spec:
  accessModes: ["ReadWriteOnce"]
  storageClassName: "standard"
  resources:
    requests:
      storage: 10Ti
EOF
}

# Wrong access mode — request ReadWriteMany on a PV that only supports ReadWriteOnce
fault_pvc_wrong_access_mode() {
  local pvc=$1 ns=$2

  kubectl delete pvc "$pvc" -n "$ns" --ignore-not-found
  kubectl apply -n "$ns" -f - <<EOF
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: $pvc
  namespace: $ns
spec:
  accessModes: ["ReadWriteMany"]
  storageClassName: "standard"
  resources:
    requests:
      storage: 1Gi
EOF
}

verify_pvc_pending() {
  local pvc=$1 ns=$2
  kubectl get pvc "$pvc" -n "$ns" -o jsonpath='{.status.phase}' | grep -q "Pending" && \
    echo "OK: PVC pending" || echo "FAIL: PVC not pending"
}
```

## Composition Patterns

```bash
# Node cordoned + taint — double scheduling block
fault_cordon_node
fault_taint_node "" "maintenance" "true" "NoSchedule"

# Affinity mismatch + insufficient resources — agent must fix both
fault_node_affinity "$DEPLOY" "$NS"
fault_excessive_cpu_request "$DEPLOY" "$NS" "32"

# PVC binding + pod quota — storage and compute blocked simultaneously
fault_pvc_wrong_class "data-pvc" "$NS"
fault_pod_quota "$NS" "2"  # from admission skill
```

## Symptom Characteristics

Medium difficulty — events provide clues but require interpretation:
- `FailedScheduling` events with reasons like "insufficient cpu" or "node(s) had taint"
- Pods in Pending state — `kubectl describe pod` shows scheduling constraints
- No container logs (containers never created)
- Agent must correlate event reasons with node state (`kubectl describe node`)
