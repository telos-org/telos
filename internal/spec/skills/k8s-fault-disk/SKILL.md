---
name: k8s-fault-disk
description: |
  Block device and storage I/O fault injection via device mapper. Creates
  disk failures that kubectl cannot — WAL corruption, checkpoint failure,
  replication lag from slow I/O, silent data loss. Critical for database
  workloads (postgres, clickhouse, openbao). Requires Khaos DaemonSet
  with host access on the target node.
metadata:
  category: fault-injection
  role: environment-generator
  difficulty: hard
  author: telos
allowed-tools: Bash(kubectl:*)
---

# Disk Fault Injection via Device Mapper

These operators create real disk failures at the block device level. The
application genuinely experiences I/O errors — not simulated, not mocked.
This tests database crash recovery, WAL integrity, checkpoint behavior,
and replication under storage failure.

All operators require a Khaos DaemonSet pod running on the target node
with `hostPID: true` and access to `/dev`, `/sys`, and device-mapper.

## Prerequisites

The Khaos DaemonSet must be deployed before any disk fault operators.

```bash
khaos_pod_on_node() {
  local node=$1
  kubectl get pods -n khaos -l app=khaos \
    --field-selector spec.nodeName="$node" \
    -o jsonpath='{.items[0].metadata.name}'
}

khaos_exec() {
  local node=$1; shift
  local pod=$(khaos_pod_on_node "$node")
  kubectl exec -n khaos "$pod" -- nsenter --mount=/proc/1/ns/mnt bash -lc "$*"
}
```

## Operators

### dm-flakey: Periodic I/O Failure

Device alternates between working and broken on a fixed schedule.
Fully deterministic — same parameters produce identical behavior.

Use cases:
- Database WAL recovery (writes fail mid-checkpoint)
- Replication lag (writes stall, replica falls behind)
- Fsync failures (data acknowledged but not persisted)

```bash
# All writes fail permanently — "disk died"
fault_disk_error_writes() {
  local node=$1
  local target_pvc_dev=$2  # e.g., /dev/loop0 backing the PVC

  khaos_exec "$node" "
    modprobe dm_flakey 2>/dev/null || true
    SECTORS=\$(blockdev --getsz $target_pvc_dev)
    dmsetup create telos_flakey0 \
      --table \"0 \$SECTORS flakey $target_pvc_dev 0 0 999999 1 error_writes\"
  "
}

# Periodic failure — up_s seconds normal, down_s seconds of errors
fault_disk_periodic_failure() {
  local node=$1
  local target_dev=$2
  local up_s=${3:-10}
  local down_s=${4:-5}
  local features=${5:-"error_writes"}  # error_writes | drop_writes

  khaos_exec "$node" "
    modprobe dm_flakey 2>/dev/null || true
    SECTORS=\$(blockdev --getsz $target_dev)
    dmsetup create telos_flakey0 \
      --table \"0 \$SECTORS flakey $target_dev 0 $up_s $down_s 1 $features\"
  "
}

# Silent data loss — writes succeed (no error) but data is dropped
# This is the worst failure mode: the application thinks it persisted data
fault_disk_drop_writes() {
  local node=$1
  local target_dev=$2

  khaos_exec "$node" "
    modprobe dm_flakey 2>/dev/null || true
    SECTORS=\$(blockdev --getsz $target_dev)
    dmsetup create telos_flakey0 \
      --table \"0 \$SECTORS flakey $target_dev 0 0 999999 1 drop_writes\"
  "
}

# Bit corruption at a specific byte offset in every write
# Tests checksum verification and data integrity checks
fault_disk_corrupt_writes() {
  local node=$1
  local target_dev=$2
  local byte_pos=${3:-32}  # which byte in the bio to corrupt

  khaos_exec "$node" "
    modprobe dm_flakey 2>/dev/null || true
    SECTORS=\$(blockdev --getsz $target_dev)
    dmsetup create telos_flakey0 \
      --table \"0 \$SECTORS flakey $target_dev 0 0 999999 4 corrupt_bio_byte $byte_pos r 1 0\"
  "
}

verify_dm_flakey() {
  local node=$1
  khaos_exec "$node" "dmsetup status telos_flakey0 2>/dev/null" && \
    echo "OK: dm-flakey active" || echo "FAIL: dm-flakey not found"
}

recover_dm_flakey() {
  local node=$1
  khaos_exec "$node" "
    umount /mnt/telos_flakey0 2>/dev/null || true
    dmsetup remove telos_flakey0 2>/dev/null || true
  "
}
```

### dm-error: Permanent Disk Death

All I/O returns errors. Simpler than dm-flakey — the disk is dead, period.
Tests failover to replica, whether the system detects and handles total
storage loss.

```bash
fault_disk_dead() {
  local node=$1
  local target_dev=$2

  khaos_exec "$node" "
    SECTORS=\$(blockdev --getsz $target_dev)
    dmsetup create telos_error0 --table \"0 \$SECTORS error\"
  "
}

recover_disk_dead() {
  local node=$1
  khaos_exec "$node" "dmsetup remove telos_error0 2>/dev/null || true"
}
```

### dm-delay: Fixed I/O Latency

Add deterministic latency to read/write operations. No jitter — every
I/O takes exactly the specified delay. Tests timeout handling, connection
pool exhaustion from slow queries, replication lag.

```bash
# Slow reads AND writes
fault_disk_slow() {
  local node=$1
  local target_dev=$2
  local read_delay_ms=${3:-500}
  local write_delay_ms=${4:-500}

  khaos_exec "$node" "
    modprobe dm_delay 2>/dev/null || true
    SECTORS=\$(blockdev --getsz $target_dev)
    dmsetup create telos_delay0 \
      --table \"0 \$SECTORS delay $target_dev 0 $read_delay_ms $target_dev 0 $write_delay_ms\"
  "
}

# Slow writes only — reads are fast, writes are slow
# Simulates degraded SSD, RAID rebuild, network-attached storage latency
fault_disk_slow_writes() {
  local node=$1
  local target_dev=$2
  local write_delay_ms=${3:-1000}

  khaos_exec "$node" "
    modprobe dm_delay 2>/dev/null || true
    SECTORS=\$(blockdev --getsz $target_dev)
    dmsetup create telos_delay0 \
      --table \"0 \$SECTORS delay $target_dev 0 0 $target_dev 0 $write_delay_ms\"
  "
}

recover_disk_slow() {
  local node=$1
  khaos_exec "$node" "dmsetup remove telos_delay0 2>/dev/null || true"
}
```

### Force Cache Drop

Drop all page cache so the application must re-read from disk. Combined
with dm-flakey, this forces the application to hit the broken disk
immediately instead of serving from cache.

```bash
fault_drop_caches() {
  local node=$1
  khaos_exec "$node" "echo 3 > /proc/sys/vm/drop_caches"
}
```

## Finding the Target Device

On local-path storage, PVCs are backed by node-local disk. To find the
device backing a specific PVC:

```bash
find_pvc_device() {
  local ns=$1 pvc=$2
  local pod=$(kubectl get pods -n "$ns" \
    -o jsonpath="{.items[?(@.spec.volumes[*].persistentVolumeClaim.claimName=='$pvc')].metadata.name}" | awk '{print $1}')
  local node=$(kubectl get pod "$pod" -n "$ns" -o jsonpath='{.spec.nodeName}')
  local pv=$(kubectl get pvc "$pvc" -n "$ns" -o jsonpath='{.spec.volumeName}')
  local host_path=$(kubectl get pv "$pv" -o jsonpath='{.spec.hostPath.path}')

  echo "node=$node pv=$pv path=$host_path"
  # For loopback-based PVs, find the loop device:
  khaos_exec "$node" "losetup -j $host_path 2>/dev/null || echo 'not a loop device'"
}
```

## Composition Patterns

```bash
# Slow disk + cache drop — force postgres to wait for every read
fault_disk_slow "$NODE" "$DEV" 200 200
fault_drop_caches "$NODE"

# Silent data loss + normal operation — worst case for data integrity
# Writes succeed but data is lost. Tests whether checksums catch it.
fault_disk_drop_writes "$NODE" "$DEV"

# Periodic disk failure during checkpoint
# Postgres checkpoints every 5 minutes. If disk fails for 10 seconds
# during a checkpoint, the checkpoint is incomplete.
fault_disk_periodic_failure "$NODE" "$DEV" 30 10 "error_writes"

# Cascading: primary disk dead + slow replica disk
# Tests failover speed AND whether replica can serve under I/O pressure
fault_disk_dead "$PRIMARY_NODE" "$PRIMARY_DEV"
fault_disk_slow_writes "$REPLICA_NODE" "$REPLICA_DEV" 500
```

## Symptom Characteristics

Hard difficulty — symptoms are application-level with no K8s-level clue:
- postgres: "PANIC: could not write to file pg_wal/..."
- clickhouse: "Code: 243. DB::Exception: Cannot write to file"
- Pod is Running, health checks may pass (if they don't touch disk)
- `kubectl describe pod` shows nothing wrong
- Events show nothing wrong
- Agent must check application logs, correlate with disk state
- May need to check `dmesg` / kernel logs on the node for I/O errors
