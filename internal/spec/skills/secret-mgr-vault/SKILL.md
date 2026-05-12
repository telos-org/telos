---
name: secret-mgr-vault
description: |
  OpenBao/Vault secret management operations. Use when managing secret stores
  via the Vault-compatible API — seal management, policy, audit, secret engines.
  Triggers on: openbao, vault, secret_manager category.
metadata:
  category: secret_manager
  protocols: vault, http
  author: telos
allowed-tools: Bash(bao:*) Bash(vault:*) Bash(curl:*)
---

# Secret Manager Operations (Vault API)

You are operating an OpenBao instance (Vault API-compatible fork).
The CLI is `bao` (or `vault` for compatibility). The API is HTTP on port 8200.

## Diagnostics

### Server status and seal state
```bash
bao status
bao status -format=json | jq '{sealed, cluster_name, version}'
```

### Health check
```bash
curl -s http://localhost:8200/v1/sys/health | jq .
```

### Audit log status
```bash
bao audit list -detailed
```

### Secret engine inventory
```bash
bao secrets list -detailed
```

### Auth method inventory
```bash
bao auth list -detailed
```

### Policy list
```bash
bao policy list
bao policy read <policy-name>
```

## Operations

### Initialize (first time)
```bash
bao operator init -key-shares=5 -key-threshold=3
```

### Unseal
```bash
bao operator unseal <key1>
bao operator unseal <key2>
bao operator unseal <key3>
```

### Enable KV secret engine
```bash
bao secrets enable -path=secret kv-v2
```

### Enable audit log
```bash
bao audit enable file file_path=/var/log/bao/audit.log
```

### Write and read a secret
```bash
bao kv put secret/myapp/config db_host=postgres db_port=5432
bao kv get secret/myapp/config
```

## Common Failure Patterns

1. **Sealed after restart**: Need threshold unseal keys — automate with transit unseal or cloud KMS
2. **Permission denied**: Check token policies, path matching (exact vs glob), token TTL
3. **High latency**: Check storage backend (Raft consensus, disk I/O), audit log overhead
4. **Audit device blocked**: If audit fails, Vault blocks all operations — check disk space, log rotation
5. **Raft leader loss**: Check cluster peers, network between nodes, storage health
