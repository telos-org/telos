---
name: cache-resp
description: |
  Redis/Valkey in-memory cache operations. Use when managing RESP protocol
  services — diagnostics, benchmarking, replication, persistence, failover.
  Triggers on: valkey, redis, resp protocol, cache category.
metadata:
  category: cache
  protocols: resp
  author: telos
allowed-tools: Bash(redis-cli:*) Bash(memtier_benchmark:*)
---

# Cache Operations (RESP Protocol)

You are operating a Redis-compatible in-memory data store (Valkey/Redis).
The wire protocol is RESP. All diagnostics use `redis-cli` and benchmarking
uses `memtier_benchmark`.

## Diagnostics

```bash
# Liveness
redis-cli -h <host> -p 6379 PING
# → PONG

# Server info
redis-cli -h <host> -p 6379 INFO server
redis-cli -h <host> -p 6379 INFO memory
redis-cli -h <host> -p 6379 INFO replication
redis-cli -h <host> -p 6379 INFO stats

# Key diagnostics
redis-cli -h <host> -p 6379 DBSIZE
redis-cli -h <host> -p 6379 SLOWLOG GET 10
redis-cli -h <host> -p 6379 CLIENT LIST
```

## Data Integrity Verification

Write a reproducible test that verifies SET/GET roundtrip:

```python
# test_data_integrity.py
import socket

def test_resp_set_get():
    """SET a key, GET it back, assert equality."""
    sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    sock.settimeout(5)
    sock.connect(("localhost", 6379))

    # SET _telos_check oracle
    sock.sendall(b"*3\r\n$3\r\nSET\r\n$12\r\n_telos_check\r\n$6\r\noracle\r\n")
    assert b"+OK" in sock.recv(64)

    # GET _telos_check
    sock.sendall(b"*2\r\n$3\r\nGET\r\n$12\r\n_telos_check\r\n")
    assert b"oracle" in sock.recv(64)
    sock.close()
```

## Benchmarking

Use `memtier_benchmark` for latency and throughput measurement:

```bash
# Balanced read/write, 20 clients, 2 threads, 10 seconds
memtier_benchmark -s <host> -p 6379 \
  --ratio 1:1 -c 20 -t 2 --test-time 10 \
  --json-out-file results.json --hide-histogram

# Read-heavy (GET only)
memtier_benchmark -s <host> -p 6379 \
  --ratio 1:0 -c 20 -t 2 --test-time 10

# Write-heavy (SET only)
memtier_benchmark -s <host> -p 6379 \
  --ratio 0:1 -c 20 -t 2 --test-time 10
```

Parse results from JSON: `jq '.["ALL STATS"].Totals.Latency["p99.00"]' results.json`

## Configuration Tuning

Key parameters to optimize:

- `maxmemory-policy`: Use `allkeys-lru` for cache, `noeviction` for data store
- `maxmemory`: Set to 80% of available memory
- `tcp-backlog`: Increase for high connection rates (default 511)
- `save`: Disable for pure cache (`save ""`), enable for persistence
- `appendonly`: Enable AOF for durability (`appendonly yes`)

```bash
redis-cli CONFIG SET maxmemory-policy allkeys-lru
redis-cli CONFIG SET maxmemory 256mb
```

## Replication

For high availability, set up replica:

```bash
# On replica
redis-cli REPLICAOF <primary-host> 6379

# Check replication status
redis-cli INFO replication
# → role:master, connected_slaves:N
```

## TLS Configuration

Valkey/Redis TLS requires cert, key, and CA files mounted from K8s secrets.

```bash
# Generate certs with SANs for headless service DNS
openssl req -x509 -newkey rsa:4096 -keyout ca-key.pem -out ca-cert.pem -days 365 -nodes \
  -subj "/CN=valkey-ca"

# Server cert — SAN must include headless service names for replication
openssl req -newkey rsa:4096 -keyout server-key.pem -out server.csr -nodes \
  -subj "/CN=valkey" \
  -addext "subjectAltName=DNS:valkey-0.valkey-headless.$NS.svc.cluster.local,DNS:valkey-1.valkey-headless.$NS.svc.cluster.local,DNS:valkey-2.valkey-headless.$NS.svc.cluster.local"

openssl x509 -req -in server.csr -CA ca-cert.pem -CAkey ca-key.pem -CAcreateserial \
  -out server-cert.pem -days 365 -copy_extensions copyall
```

Server config:
```
tls-port 6379
port 0
tls-cert-file /tls/server-cert.pem
tls-key-file /tls/server-key.pem
tls-ca-cert-file /tls/ca-cert.pem
tls-auth-clients yes
tls-replication yes
```

**Common TLS failure**: certificate SAN mismatch. Replicas connect via
`valkey-N.valkey-headless.<ns>.svc.cluster.local` — the cert must include
these DNS names, not just `valkey-0.valkey.<ns>.svc.cluster.local`.

## Encryption at Rest

The default cloud k3s StorageClass should not be treated as application-level
encryption. Options:

1. **Application-level encryption**: Encrypt values before
   writing. Use `DUMP`/`RESTORE` with encrypted payloads, or encrypt at the
   application layer before `SET`.
2. **gocryptfs/FUSE**: Requires `privileged: true` and a sidecar that mounts
   the encrypted filesystem before Valkey starts. **Race conditions are common** —
   the sidecar must be fully mounted before the Valkey container reads the data dir.
   This approach is fragile in containers and should be avoided when possible.
3. **dm-crypt/LUKS**: Requires `privileged` init container with `cryptsetup`.
   Not viable in standard k8s pod security policies.

For cloud environments, application-level encryption is the practical path. Do
not spend time on gocryptfs mount propagation — it's a dead end without
privileged containers.

## Access Control (ACLs)

When `access_control: rbac` is required, configure Valkey ACLs:

```bash
# Create application user with limited permissions
redis-cli ACL SETUSER app_user on >password ~app:* +@read +@write -@admin

# Create read-only user
redis-cli ACL SETUSER readonly on >password ~* +@read -@write -@admin

# Verify
redis-cli ACL LIST
redis-cli ACL WHOAMI
```

Default user should have a strong password:
```bash
redis-cli CONFIG SET requirepass "strong-password-here"
```

## Kubernetes Patterns

See [references/k8s-patterns.md](references/k8s-patterns.md) for Deployment, Service,
and StatefulSet patterns specific to Valkey/Redis on Kubernetes.

Key StatefulSet gotchas:
- Use `valkey-headless` Service (clusterIP: None) for replication DNS
- Replicas connect via `valkey-N.valkey-headless.<ns>.svc.cluster.local`
- Init container for replication should handle pre-existing master state gracefully
- `fsGroup` in pod security context handles PVC permissions — don't `chmod` as root
