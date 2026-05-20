---
name: k8s-fault-process
description: |
  Process-level fault injection — SIGSTOP/SIGCONT process freeze, cgroups v2
  resource starvation (cpu.max, memory.high, io.max, pids.max), clock skew
  via libfaketime, and eBPF syscall error injection. Creates "pod is Running
  but degraded" faults — the hardest class to diagnose. Requires Khaos
  DaemonSet for cgroups/eBPF, libfaketime for clock faults.
metadata:
  category: fault-injection
  difficulty: hard
  author: telos
allowed-tools: Bash(kubectl:*)
---

# Process-Level Fault Injection

These operators degrade a process without killing it. The pod shows Running,
health checks may pass, but the application is broken in subtle ways —
frozen, starved, confused about time, or experiencing phantom I/O errors.
This creates the hardest class of faults for agents to diagnose.

## Prerequisites

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

# Resolve the host PID of a container's main process
resolve_host_pid() {
  local ns=$1 deploy=$2
  local pod=$(kubectl get pods -n "$ns" -l app="$deploy" \
    -o jsonpath='{.items[0].metadata.name}')
  local node=$(kubectl get pod "$pod" -n "$ns" -o jsonpath='{.spec.nodeName}')
  local container_id=$(kubectl get pod "$pod" -n "$ns" \
    -o jsonpath='{.status.containerStatuses[0].containerID}' | sed 's|containerd://||')

  # Resolve via /proc scan on the node
  local kpod=$(khaos_pod_on_node "$node")
  local short=${container_id:0:12}
  local pid=$(kubectl exec -n khaos "$kpod" -- sh -c \
    "grep -l '$short' /proc/*/cgroup 2>/dev/null | sed -n 's#.*/proc/\([0-9]*\)/cgroup#\1#p' | head -1")

  echo "node=$node pid=$pid pod=$pod"
}
```

## Operators

### Process Freeze (SIGSTOP / SIGCONT)

Freeze a process completely. It stops executing but remains in memory.
The container shows Running, the process exists, but it does nothing.
Simulates GC pauses, deadlocks, process hangs.

Fully deterministic: SIGSTOP always freezes, SIGCONT always resumes.

```bash
fault_process_freeze() {
  local ns=$1 deploy=$2
  eval $(resolve_host_pid "$ns" "$deploy")
  # node and pid are now set

  khaos_exec "$node" "kill -STOP $pid"
}

recover_process_freeze() {
  local ns=$1 deploy=$2
  eval $(resolve_host_pid "$ns" "$deploy")

  khaos_exec "$node" "kill -CONT $pid"
}

verify_process_frozen() {
  local ns=$1 deploy=$2
  eval $(resolve_host_pid "$ns" "$deploy")

  local state=$(khaos_exec "$node" "cat /proc/$pid/status | grep State")
  echo "$state"
  echo "$state" | grep -q "stopped" && \
    echo "OK: process frozen" || echo "FAIL: process not frozen"
}
```

Use cases:
- Leadership election testing: freeze postgres primary, does replica promote?
- Connection timeout testing: freeze a service, do dependent services handle it?
- Split-brain: freeze one member of a cluster, does quorum hold?

### Targeted Process Kill

Kill a specific process inside a container without restarting the pod.
The container stays Running but the application is dead. Tests process
supervision, restart logic, PID 1 behavior.

```bash
fault_kill_main_process() {
  local ns=$1 deploy=$2
  local signal=${3:-"KILL"}  # KILL, TERM, SEGV, ABRT
  eval $(resolve_host_pid "$ns" "$deploy")

  khaos_exec "$node" "kill -$signal $pid"
}

# Kill a child process (e.g., a specific postgres backend, a worker thread)
fault_kill_child() {
  local ns=$1 deploy=$2 child_name=$3
  local pod=$(kubectl get pods -n "$ns" -l app="$deploy" \
    -o jsonpath='{.items[0].metadata.name}')

  kubectl exec "$pod" -n "$ns" -- sh -c "pkill -f '$child_name'"
}
```

### cgroups v2: CPU Throttle

Hard CPU cap via cgroups. More precise than kubectl resource limits because
it's applied directly, not through the scheduler's request/limit mechanism.

"This process gets 50ms of CPU per 100ms period" = 50% of one core, hard cap.
The application slows down but doesn't crash. Queries take 2x longer, health
checks may start timing out.

```bash
fault_cpu_throttle_cgroup() {
  local ns=$1 deploy=$2
  local quota_us=${3:-50000}   # microseconds of CPU per period
  local period_us=${4:-100000}  # period in microseconds
  eval $(resolve_host_pid "$ns" "$deploy")

  # Find the cgroup for this PID
  local cgroup=$(khaos_exec "$node" "cat /proc/$pid/cgroup | grep -o '[^:]*$' | head -1")

  khaos_exec "$node" "echo '$quota_us $period_us' > /sys/fs/cgroup${cgroup}/cpu.max"
}

recover_cpu_throttle_cgroup() {
  local ns=$1 deploy=$2
  eval $(resolve_host_pid "$ns" "$deploy")
  local cgroup=$(khaos_exec "$node" "cat /proc/$pid/cgroup | grep -o '[^:]*$' | head -1")

  khaos_exec "$node" "echo 'max 100000' > /sys/fs/cgroup${cgroup}/cpu.max"
}
```

### cgroups v2: Memory Pressure

Set memory.high — soft memory limit. The kernel aggressively reclaims pages
but doesn't kill the process. The application slows down as every allocation
triggers reclaim. Much more realistic than OOM-kill.

```bash
fault_memory_pressure() {
  local ns=$1 deploy=$2
  local memory_high=${3:-"64M"}  # soft limit
  eval $(resolve_host_pid "$ns" "$deploy")
  local cgroup=$(khaos_exec "$node" "cat /proc/$pid/cgroup | grep -o '[^:]*$' | head -1")

  khaos_exec "$node" "echo '$memory_high' > /sys/fs/cgroup${cgroup}/memory.high"
}

recover_memory_pressure() {
  local ns=$1 deploy=$2
  eval $(resolve_host_pid "$ns" "$deploy")
  local cgroup=$(khaos_exec "$node" "cat /proc/$pid/cgroup | grep -o '[^:]*$' | head -1")

  khaos_exec "$node" "echo 'max' > /sys/fs/cgroup${cgroup}/memory.high"
}
```

### cgroups v2: I/O Bandwidth Cap

Hard limit on I/O throughput. Database writes get queued, WAL flushing
falls behind, replication lag grows. Everything works but slowly.

```bash
fault_io_throttle() {
  local ns=$1 deploy=$2
  local write_bps=${3:-"1048576"}  # 1MB/s write limit
  local read_bps=${4:-"1048576"}   # 1MB/s read limit
  eval $(resolve_host_pid "$ns" "$deploy")
  local cgroup=$(khaos_exec "$node" "cat /proc/$pid/cgroup | grep -o '[^:]*$' | head -1")

  # Find the major:minor of the block device
  local dev_maj_min=$(khaos_exec "$node" "lsblk -no MAJ:MIN /dev/sda | head -1 | tr -d ' '")

  khaos_exec "$node" \
    "echo '$dev_maj_min rbps=$read_bps wbps=$write_bps' > /sys/fs/cgroup${cgroup}/io.max"
}

recover_io_throttle() {
  local ns=$1 deploy=$2
  eval $(resolve_host_pid "$ns" "$deploy")
  local cgroup=$(khaos_exec "$node" "cat /proc/$pid/cgroup | grep -o '[^:]*$' | head -1")
  local dev_maj_min=$(khaos_exec "$node" "lsblk -no MAJ:MIN /dev/sda | head -1 | tr -d ' '")

  khaos_exec "$node" \
    "echo '$dev_maj_min rbps=max wbps=max' > /sys/fs/cgroup${cgroup}/io.max"
}
```

### cgroups v2: Fork Exhaustion

Limit number of PIDs in the cgroup. Set it to current count + 0 and the
next fork/thread creation fails. Tests connection pool exhaustion, thread
limit errors, "cannot create new thread" failures.

```bash
fault_pid_exhaustion() {
  local ns=$1 deploy=$2
  eval $(resolve_host_pid "$ns" "$deploy")
  local cgroup=$(khaos_exec "$node" "cat /proc/$pid/cgroup | grep -o '[^:]*$' | head -1")

  # Set pids.max to current count — no new processes/threads
  local current=$(khaos_exec "$node" "cat /sys/fs/cgroup${cgroup}/pids.current")
  khaos_exec "$node" "echo '$current' > /sys/fs/cgroup${cgroup}/pids.max"
}

recover_pid_exhaustion() {
  local ns=$1 deploy=$2
  eval $(resolve_host_pid "$ns" "$deploy")
  local cgroup=$(khaos_exec "$node" "cat /proc/$pid/cgroup | grep -o '[^:]*$' | head -1")

  khaos_exec "$node" "echo 'max' > /sys/fs/cgroup${cgroup}/pids.max"
}
```

### Clock Skew via libfaketime

Make a process see a different time. TLS certificates expire, TOTP tokens
fail, scheduled jobs fire at wrong times, log timestamps become impossible.
Fully deterministic — same offset produces same time every run.

```bash
# Requires libfaketime installed in the container or injected via Khaos
fault_clock_skew() {
  local ns=$1 deploy=$2
  local offset=${3:-"+365d"}  # +365d, -1h, +2y, etc.

  local pod=$(kubectl get pods -n "$ns" -l app="$deploy" \
    -o jsonpath='{.items[0].metadata.name}')

  # Inject libfaketime via environment variable
  # Requires restart to take effect
  kubectl set env deployment/"$deploy" -n "$ns" \
    LD_PRELOAD=/usr/lib/faketime/libfaketime.so.1 \
    FAKETIME="$offset"
}

recover_clock_skew() {
  local ns=$1 deploy=$2

  kubectl set env deployment/"$deploy" -n "$ns" \
    LD_PRELOAD- FAKETIME-
}
```

When libfaketime is not in the container image, use the kernel approach —
set the clock in the container's time namespace:

```bash
fault_clock_skew_kernel() {
  local ns=$1 deploy=$2
  local offset_seconds=${3:-"31536000"}  # 1 year in seconds
  eval $(resolve_host_pid "$ns" "$deploy")

  # Adjust the container's CLOCK_MONOTONIC offset via time namespace
  # Note: requires CAP_SYS_TIME in the container or nsenter from host
  khaos_exec "$node" "
    nsenter -t $pid -T date -s @\$(( \$(date +%s) + $offset_seconds ))
  " 2>/dev/null || echo "WARN: clock skew requires time namespace or CAP_SYS_TIME"
}
```

### eBPF Syscall Error Injection

Intercept specific syscalls for specific PIDs and return error codes.
The process genuinely experiences a real syscall failure. Deterministic
when using `fail-nth` or targeting specific syscalls with 100% probability.

Requires bpftrace or a precompiled BPF injector on the Khaos DaemonSet.

```bash
# Make write() return EIO for a specific process
fault_bpf_write_eio() {
  local ns=$1 deploy=$2
  eval $(resolve_host_pid "$ns" "$deploy")

  khaos_exec "$node" "
    bpftrace -e '
      kprobe:ksys_write /pid == $pid/ {
        override(-5);
      }
    ' &
    echo \$! > /tmp/bpf_inject_pid
  " 2>/dev/null || echo "WARN: bpftrace not available on khaos node"
}

# Make connect() return ECONNREFUSED for a specific process
fault_bpf_connect_refused() {
  local ns=$1 deploy=$2
  eval $(resolve_host_pid "$ns" "$deploy")

  khaos_exec "$node" "
    bpftrace -e '
      kretprobe:__sys_connect /pid == $pid/ {
        override(-111);
      }
    ' &
    echo \$! > /tmp/bpf_inject_pid
  "
}

# Make read() return EIO every Nth call (fully deterministic)
fault_bpf_read_nth() {
  local ns=$1 deploy=$2 nth=${3:-10}
  eval $(resolve_host_pid "$ns" "$deploy")

  khaos_exec "$node" "
    bpftrace -e '
      @count[$pid] = count();
      kretprobe:ksys_read /pid == $pid && @count[$pid] % $nth == 0/ {
        override(-5);
      }
    ' &
    echo \$! > /tmp/bpf_inject_pid
  "
}

# Alternatively, use the kernel's built-in fail-nth (simpler, no bpftrace needed)
fault_kernel_fail_nth() {
  local ns=$1 deploy=$2 nth=${3:-10}
  eval $(resolve_host_pid "$ns" "$deploy")

  # Enable fail_make_request and scope to this PID
  khaos_exec "$node" "
    echo 1 > /proc/$pid/make-it-fail
    echo 100 > /sys/kernel/debug/fail_make_request/probability
    echo 1 > /sys/kernel/debug/fail_make_request/interval
    echo -1 > /sys/kernel/debug/fail_make_request/times
    echo Y > /sys/kernel/debug/fail_make_request/task-filter
  "
}

recover_bpf_inject() {
  local node=$1
  khaos_exec "$node" "
    [ -f /tmp/bpf_inject_pid ] && kill \$(cat /tmp/bpf_inject_pid) 2>/dev/null
    rm -f /tmp/bpf_inject_pid
    # Reset kernel fault injection
    echo 0 > /sys/kernel/debug/fail_make_request/probability 2>/dev/null || true
  "
}
```

## Composition Patterns

```bash
# Freeze primary + slow replica I/O — agent must promote replica under duress
fault_process_freeze "$NS" "postgres-primary"
fault_io_throttle "$NS" "postgres-replica" "524288"  # 512KB/s

# Memory pressure + CPU throttle — cascading degradation
# App slows down, then starts swapping, then health checks time out
fault_memory_pressure "$NS" "clickhouse" "128M"
fault_cpu_throttle_cgroup "$NS" "clickhouse" "25000" "100000"  # 25% CPU

# Clock skew + BPF write errors — TLS cert expires AND disk fails
# Agent must diagnose two independent root causes
fault_clock_skew "$NS" "openbao" "+2y"
fault_bpf_write_eio "$NS" "openbao"

# Fork exhaustion + process freeze on a dependency
# App can't create new connections AND its database is frozen
fault_pid_exhaustion "$NS" "webapp"
fault_process_freeze "$NS" "postgres"

# Progressive degradation: I/O throttle → memory pressure → CPU throttle
# Each layer makes the previous one worse
fault_io_throttle "$NS" "postgres" "2097152"      # 2MB/s I/O
fault_memory_pressure "$NS" "postgres" "256M"      # force page reclaim
fault_cpu_throttle_cgroup "$NS" "postgres" "50000"  # 50% CPU
```

## Symptom Characteristics

Hard difficulty — the hardest class of faults:
- Pod is Running, container is Running, health checks may pass
- `kubectl describe pod` shows nothing wrong
- `kubectl get events` shows nothing wrong
- Application logs show errors but root cause is below the application layer
- SIGSTOP: connection timeouts, no new log entries, process exists but silent
- cgroups: slow responses, timeout errors, "too many connections" from thread exhaustion
- Clock skew: "certificate has expired", "token invalid", timestamp anomalies
- BPF injection: "I/O error" with healthy disk, "connection refused" with healthy network
- Agent must check process state, cgroup limits, system time, kernel logs
