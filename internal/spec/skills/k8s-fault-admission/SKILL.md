---
name: k8s-fault-admission
description: |
  Admission control fault injection — quota exhaustion, missing service accounts,
  RBAC denials. These faults prevent resources from being created or modified.
  Hard difficulty: symptoms are implicit (pods stuck in Pending, API rejections)
  with no direct error in container logs.
metadata:
  category: fault-injection
  role: environment-generator
  difficulty: hard
  source: cloud-opsbench
  author: telos
allowed-tools: Bash(kubectl:*)
---

# Admission Control Fault Injection

Faults in this category cause the Kubernetes admission controller to reject
or constrain resource creation. The agent sees pods stuck in Pending or API
errors with no container-level logs — diagnosis requires cross-layer reasoning
from events to admission policy.

Each operator follows the (P, A, S) triplet:
- **P** (Prerequisites): cluster state needed before injection
- **A** (Fault Artifact): the defective configuration
- **S** (Activation Sequence): ordered steps to apply P then A

## Operators

### NamespaceCPUQuotaExceeded

Pods requesting more CPU than the namespace quota allows get rejected.
The pod never starts — no container logs, only events.

```bash
fault_cpu_quota() {
  local ns=$1
  local quota_cpu=${2:-"100m"}  # deliberately tight

  # P: namespace exists
  # A: ResourceQuota with tight CPU limit
  kubectl apply -n "$ns" -f - <<EOF
apiVersion: v1
kind: ResourceQuota
metadata:
  name: tight-cpu-quota
  namespace: $ns
spec:
  hard:
    requests.cpu: "$quota_cpu"
    limits.cpu: "$quota_cpu"
EOF

  # S: any subsequent deployment requesting more CPU will be rejected
}

# Postcondition: quota is active
verify_cpu_quota() {
  local ns=$1
  local used=$(kubectl get resourcequota tight-cpu-quota -n "$ns" \
    -o jsonpath='{.status.used.requests\.cpu}' 2>/dev/null)
  [ -n "$used" ] && echo "OK: CPU quota active, used=$used" || echo "FAIL: quota not found"
}
```

### NamespaceMemoryQuotaExceeded

Same pattern as CPU but for memory. Pods requesting more memory than
the quota are rejected at admission.

```bash
fault_memory_quota() {
  local ns=$1
  local quota_mem=${2:-"64Mi"}  # deliberately tight

  kubectl apply -n "$ns" -f - <<EOF
apiVersion: v1
kind: ResourceQuota
metadata:
  name: tight-memory-quota
  namespace: $ns
spec:
  hard:
    requests.memory: "$quota_mem"
    limits.memory: "$quota_mem"
EOF
}

verify_memory_quota() {
  local ns=$1
  local used=$(kubectl get resourcequota tight-memory-quota -n "$ns" \
    -o jsonpath='{.status.used.requests\.memory}' 2>/dev/null)
  [ -n "$used" ] && echo "OK: memory quota active, used=$used" || echo "FAIL: quota not found"
}
```

### NamespacePodQuotaExceeded

Limits the total number of pods in a namespace. Once the limit is reached,
new pods (including from Deployments/StatefulSets) are rejected.

```bash
fault_pod_quota() {
  local ns=$1
  local max_pods=${2:-"2"}  # allow only 2 pods total

  kubectl apply -n "$ns" -f - <<EOF
apiVersion: v1
kind: ResourceQuota
metadata:
  name: tight-pod-quota
  namespace: $ns
spec:
  hard:
    pods: "$max_pods"
EOF
}

verify_pod_quota() {
  local ns=$1
  local hard=$(kubectl get resourcequota tight-pod-quota -n "$ns" \
    -o jsonpath='{.status.hard.pods}' 2>/dev/null)
  local used=$(kubectl get resourcequota tight-pod-quota -n "$ns" \
    -o jsonpath='{.status.used.pods}' 2>/dev/null)
  echo "Pod quota: $used/$hard"
  [ "$used" -ge "$hard" ] 2>/dev/null && echo "OK: quota exhausted" || echo "INFO: quota not yet exhausted"
}
```

### NamespaceStorageQuotaExceeded

Limits total PVC storage. New PVCs requesting storage beyond the quota
are rejected — StatefulSets with volumeClaimTemplates will fail to scale.

```bash
fault_storage_quota() {
  local ns=$1
  local quota_storage=${2:-"100Mi"}

  kubectl apply -n "$ns" -f - <<EOF
apiVersion: v1
kind: ResourceQuota
metadata:
  name: tight-storage-quota
  namespace: $ns
spec:
  hard:
    requests.storage: "$quota_storage"
    persistentvolumeclaims: "1"
EOF
}

verify_storage_quota() {
  local ns=$1
  kubectl get resourcequota tight-storage-quota -n "$ns" \
    -o jsonpath='{.status.hard}' 2>/dev/null
}
```

### MissingServiceAccount

Delete or replace the service account referenced by pods. Pods referencing
a nonexistent SA are rejected by admission. Subtle because the SA name
appears correct in the manifest but doesn't exist.

```bash
fault_missing_sa() {
  local ns=$1
  local sa_name=${2:-"app-service-account"}

  # P: pod/deployment references this SA
  # A: delete the SA so the reference is dangling
  kubectl delete serviceaccount "$sa_name" -n "$ns" --ignore-not-found

  # Alternative: create a deployment that references a nonexistent SA
  # kubectl patch deployment <name> -n "$ns" --type=json \
  #   -p '[{"op":"replace","path":"/spec/template/spec/serviceAccountName","value":"nonexistent-sa"}]'
}

verify_missing_sa() {
  local ns=$1
  local sa_name=${2:-"app-service-account"}
  kubectl get serviceaccount "$sa_name" -n "$ns" 2>&1 | grep -q "not found" && \
    echo "OK: SA missing as expected" || echo "FAIL: SA still exists"
}
```

### RBAC Denial

Remove or restrict ClusterRoleBinding/RoleBinding so the service account
lacks permissions for operations the application needs (e.g., list pods,
read secrets).

```bash
fault_rbac_deny() {
  local ns=$1
  local binding_name=$2

  # A: delete the role binding
  kubectl delete rolebinding "$binding_name" -n "$ns" --ignore-not-found
  kubectl delete clusterrolebinding "$binding_name" --ignore-not-found
}

# Alternative: create a restrictive role that overrides
fault_rbac_restrict() {
  local ns=$1
  local sa_name=$2

  kubectl apply -n "$ns" -f - <<EOF
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: deny-all
  namespace: $ns
rules: []
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: restrict-$sa_name
  namespace: $ns
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: deny-all
subjects:
- kind: ServiceAccount
  name: $sa_name
  namespace: $ns
EOF
}
```

## Composition Patterns

Chain admission faults for compound difficulty:

```bash
# CPU + memory quota together — agent must understand both constraints
fault_cpu_quota "$NS" "200m"
fault_memory_quota "$NS" "128Mi"

# Pod quota + tight resource quota — can't scale AND can't overcommit
fault_pod_quota "$NS" "3"
fault_cpu_quota "$NS" "500m"

# Missing SA + RBAC denial — even if agent recreates the SA, it has no permissions
fault_missing_sa "$NS" "app-sa"
fault_rbac_restrict "$NS" "app-sa"
```

## Symptom Characteristics

These faults produce **implicit symptoms**:
- No container logs (containers never start)
- Events show "forbidden" or "exceeded quota" messages
- Pods stuck in Pending with no scheduling information
- Agent must check `kubectl get events` and `kubectl describe` to diagnose
- Cross-layer reasoning required: event → admission policy → quota/RBAC → fix
