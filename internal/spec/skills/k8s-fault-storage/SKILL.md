---
name: k8s-fault-storage
description: |
  Storage and volume fault injection — PVC binding failures, volume mount
  permission denied, wrong storage class, data corruption via volume swaps.
  Medium-hard difficulty: storage faults often surface as application errors
  (permission denied, I/O error) rather than K8s-level failures.
metadata:
  category: fault-injection
  difficulty: medium
  source: cloud-opsbench
  author: telos
allowed-tools: Bash(kubectl:*)
---

# Storage Fault Injection

Faults in this category break the storage layer. PVCs fail to bind, volumes
mount with wrong permissions, or data directories become inaccessible. These
faults often present as application-level errors rather than K8s-level
failures, requiring the agent to trace from application logs back to the
storage subsystem.

## Operators

### VolumeMountPermissionDenied

Container runs as non-root but the volume has root-only permissions. The
application can't write to its data directory. Pod starts but the application
crashes with permission errors.

```bash
fault_volume_permission() {
  local deploy=$1 ns=$2

  # Set pod securityContext to run as non-root with a UID that can't write
  # to the volume's default root-owned directories
  kubectl patch deployment "$deploy" -n "$ns" --type=json -p "[
    {\"op\":\"add\",\"path\":\"/spec/template/spec/securityContext\",\"value\":{
      \"runAsUser\":65534,
      \"runAsGroup\":65534,
      \"fsGroup\":65534
    }},
    {\"op\":\"add\",\"path\":\"/spec/template/spec/containers/0/securityContext\",\"value\":{
      \"readOnlyRootFilesystem\":true,
      \"allowPrivilegeEscalation\":false
    }}
  ]"
}

verify_permission_denied() {
  local deploy=$1 ns=$2
  local pod=$(kubectl get pods -n "$ns" -l app="$deploy" -o jsonpath='{.items[0].metadata.name}')
  kubectl logs "$pod" -n "$ns" --tail=10 2>&1 | grep -qi "permission denied" && \
    echo "OK: permission denied in logs" || echo "WAIT: no permission error yet"
}
```

### ReadOnlyRootFilesystem

Enable readOnlyRootFilesystem when the application writes to paths
outside of mounted volumes. Application crashes trying to write temp files,
logs, or PID files.

```bash
fault_readonly_rootfs() {
  local deploy=$1 ns=$2

  kubectl patch deployment "$deploy" -n "$ns" --type=json -p "[
    {\"op\":\"add\",\"path\":\"/spec/template/spec/containers/0/securityContext\",\"value\":{
      \"readOnlyRootFilesystem\":true
    }}
  ]"
}
```

### Wrong StorageClass

Change PVC storageClassName to one that doesn't exist or has different
properties. PVC stays Pending (if class doesn't exist) or provisions
storage with wrong characteristics (if class exists but is wrong type).

```bash
fault_wrong_storage_class() {
  local sts=$1 ns=$2
  local wrong_class=${3:-"nonexistent-storage"}

  # For StatefulSets, we need to delete and recreate with new VCT
  # This is destructive — but that's the point for fault injection
  local replicas=$(kubectl get sts "$sts" -n "$ns" -o jsonpath='{.spec.replicas}')
  kubectl scale sts "$sts" -n "$ns" --replicas=0

  # Delete existing PVCs
  kubectl get pvc -n "$ns" -l app="$sts" -o name | xargs -I{} kubectl delete {} -n "$ns"

  # Create PVCs with wrong storage class
  for i in $(seq 0 $((replicas - 1))); do
    kubectl apply -n "$ns" -f - <<EOF
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: data-${sts}-${i}
  namespace: $ns
  labels:
    app: $sts
spec:
  accessModes: ["ReadWriteOnce"]
  storageClassName: "$wrong_class"
  resources:
    requests:
      storage: 1Gi
EOF
  done

  kubectl scale sts "$sts" -n "$ns" --replicas="$replicas"
}
```

### Volume Mount Path Conflict

Mount an emptyDir over the application's critical data directory, effectively
hiding the real data. Pod starts fine but sees empty data directory.

```bash
fault_shadow_mount() {
  local deploy=$1 ns=$2
  local mount_path=${3:-"/var/lib/postgresql/data"}

  kubectl patch deployment "$deploy" -n "$ns" --type=json -p "[
    {\"op\":\"add\",\"path\":\"/spec/template/spec/volumes/-\",\"value\":{
      \"name\":\"shadow-volume\",
      \"emptyDir\":{}
    }},
    {\"op\":\"add\",\"path\":\"/spec/template/spec/containers/0/volumeMounts/-\",\"value\":{
      \"name\":\"shadow-volume\",
      \"mountPath\":\"$mount_path\"
    }}
  ]"
}

verify_shadow_mount() {
  local deploy=$1 ns=$2
  local mount_path=${3:-"/var/lib/postgresql/data"}
  local pod=$(kubectl get pods -n "$ns" -l app="$deploy" -o jsonpath='{.items[0].metadata.name}')
  kubectl exec "$pod" -n "$ns" -- ls "$mount_path" 2>/dev/null | wc -l | \
    xargs -I{} echo "Files at $mount_path: {} (0 = shadow mount working)"
}
```

### ConfigMap/Secret Volume Corruption

Replace the content of a ConfigMap or Secret that's mounted as a volume.
Application reads wrong configuration from the mounted file.

```bash
fault_corrupt_configmap() {
  local cm=$1 ns=$2
  local key=$3
  local corrupt_value=$4

  kubectl patch configmap "$cm" -n "$ns" --type=json -p "[
    {\"op\":\"replace\",\"path\":\"/data/$key\",\"value\":\"$corrupt_value\"}
  ]"
  # ConfigMap updates propagate to mounted volumes within ~1 minute
}

fault_corrupt_secret() {
  local secret=$1 ns=$2
  local key=$3
  local corrupt_value=$4

  local encoded=$(echo -n "$corrupt_value" | base64)
  kubectl patch secret "$secret" -n "$ns" --type=json -p "[
    {\"op\":\"replace\",\"path\":\"/data/$key\",\"value\":\"$encoded\"}
  ]"
}

# Delete a critical ConfigMap entirely — pods with volumeMount will fail to start
fault_delete_configmap() {
  local cm=$1 ns=$2
  kubectl delete configmap "$cm" -n "$ns"
}
```

## Composition Patterns

```bash
# Permission denied + read-only rootfs — two layers of write failure
fault_volume_permission "$DEPLOY" "$NS"
fault_readonly_rootfs "$DEPLOY" "$NS"

# Shadow mount + corrupt configmap — data directory hidden AND config wrong
fault_shadow_mount "$DEPLOY" "$NS" "/var/lib/postgresql/data"
fault_corrupt_configmap "postgres-config" "$NS" "postgresql.conf" "invalid = config"

# Wrong storage class + volume permission — PVC pending AND when fixed, permissions wrong
fault_wrong_storage_class "$STS" "$NS" "premium-ssd"
fault_volume_permission "$STS" "$NS"
```

## Symptom Characteristics

Medium-hard difficulty:
- Application-level errors (permission denied, I/O error, "no such file")
- Pod may be Running but application is unhealthy
- PVC Pending is visible but the reason requires checking StorageClass
- Shadow mounts are very subtle — pod looks healthy, data is just empty
- ConfigMap corruption is time-delayed (kubelet sync interval)
- Agent must trace: app error → volume mount → PVC/ConfigMap → fix
