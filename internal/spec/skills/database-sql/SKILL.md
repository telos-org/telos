---
name: database-sql
description: |
  PostgreSQL database operations. Use when managing SQL protocol services —
  diagnostics, benchmarking, replication, backup, WAL configuration.
  Triggers on: postgres, postgresql, sql protocol, database category.
metadata:
  category: database
  protocols: sql
  author: telos
allowed-tools: Bash(psql:*) Bash(pgbench:*) Bash(pg_isready:*)
---

# Database Operations (SQL Protocol)

You are operating a PostgreSQL database. The wire protocol is SQL.
Diagnostics use `psql`, benchmarking uses `pgbench`, liveness uses `pg_isready`.

## Diagnostics

```bash
# Liveness
pg_isready -h <host> -p 5432

# Basic connectivity
psql -h <host> -p 5432 -U postgres -c "SELECT 1"

# Active queries
psql -U postgres -c "SELECT pid, state, query FROM pg_stat_activity WHERE state != 'idle'"

# Table statistics
psql -U postgres -c "SELECT schemaname, relname, n_tup_ins, n_tup_upd, n_tup_del FROM pg_stat_user_tables"

# Settings
psql -U postgres -c "SELECT name, setting, unit FROM pg_settings WHERE name IN ('shared_buffers', 'work_mem', 'max_connections', 'wal_level')"

# Background writer stats
psql -U postgres -c "SELECT * FROM pg_stat_bgwriter"
```

## Data Integrity Verification

Write a reproducible test that verifies INSERT/SELECT roundtrip:

```python
# test_data_integrity.py
import subprocess

def test_sql_roundtrip():
    """INSERT a row, SELECT it back, assert equality."""
    result = subprocess.run(
        ["psql", "-h", "localhost", "-p", "5432", "-U", "postgres", "-t", "-c",
         "CREATE TABLE IF NOT EXISTS _telos_check(k TEXT PRIMARY KEY, v TEXT); "
         "INSERT INTO _telos_check VALUES('pvg','oracle') "
         "ON CONFLICT(k) DO UPDATE SET v='oracle'; "
         "SELECT v FROM _telos_check WHERE k='pvg';"],
        capture_output=True, text=True, timeout=15,
    )
    assert "oracle" in result.stdout.strip()
```

## Benchmarking

Use `pgbench` for latency and throughput measurement:

```bash
# Initialize benchmark tables
pgbench -i -h <host> -p 5432 -U postgres postgres

# Select-only benchmark (10 clients, 10 seconds)
pgbench -h <host> -p 5432 -U postgres -S -T 10 -c 10 postgres

# Mixed read/write benchmark
pgbench -h <host> -p 5432 -U postgres -T 10 -c 10 postgres

# With per-transaction latency log
pgbench -h <host> -p 5432 -U postgres -S -T 10 -c 10 --log postgres
```

Parse p99 from transaction log:
```bash
awk '{print $3}' pgbench_log.* | sort -n | awk 'NR==int(0.99*NR){print $1/1000 " ms"}'
```

## Configuration Tuning

Key parameters for performance:

- `shared_buffers`: 25% of available RAM
- `work_mem`: 4MB per connection (increase for complex queries)
- `effective_cache_size`: 75% of available RAM
- `max_connections`: Size for expected workload (default 100)
- `wal_level`: `replica` for replication, `logical` for CDC
- `checkpoint_completion_target`: 0.9

```sql
ALTER SYSTEM SET shared_buffers = '256MB';
ALTER SYSTEM SET work_mem = '4MB';
SELECT pg_reload_conf();
```

## Backup

```bash
# Logical backup
pg_dump -h <host> -U postgres -Fc postgres > backup.dump

# Restore
pg_restore -h <host> -U postgres -d postgres backup.dump
```

## Access Control (Product-Level RBAC)

When the `access_control: rbac` constraint is declared, implement PostgreSQL
role-based access control. This is separate from K8s RBAC (which the platform
handles). You must create application-specific roles with least privilege.

```sql
-- Application role (read-write, no superuser)
CREATE ROLE app_readwrite WITH LOGIN PASSWORD 'changeme' NOSUPERUSER;
GRANT CONNECT ON DATABASE postgres TO app_readwrite;
GRANT USAGE ON SCHEMA public TO app_readwrite;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO app_readwrite;
ALTER DEFAULT PRIVILEGES IN SCHEMA public
  GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO app_readwrite;

-- Read-only role
CREATE ROLE app_readonly WITH LOGIN PASSWORD 'changeme' NOSUPERUSER;
GRANT CONNECT ON DATABASE postgres TO app_readonly;
GRANT USAGE ON SCHEMA public TO app_readonly;
GRANT SELECT ON ALL TABLES IN SCHEMA public TO app_readonly;
ALTER DEFAULT PRIVILEGES IN SCHEMA public
  GRANT SELECT ON TABLES TO app_readonly;

-- Audit role (read-only on audit schema)
CREATE ROLE auditor WITH LOGIN PASSWORD 'changeme' NOSUPERUSER;
GRANT CONNECT ON DATABASE postgres TO auditor;
GRANT USAGE ON SCHEMA audit TO auditor;
GRANT SELECT ON ALL TABLES IN SCHEMA audit TO auditor;

-- Replication role
CREATE ROLE replicator WITH LOGIN REPLICATION PASSWORD 'changeme' NOSUPERUSER;
```

Authentication: use `scram-sha-256` in `pg_hba.conf`. Never use `trust` or `md5`.

Verify with:
```sql
SELECT rolname, rolsuper, rolinherit, rolcreatedb, rolcanlogin, rolreplication
FROM pg_roles WHERE rolname NOT LIKE 'pg_%';
```

## Kubernetes Patterns

See [references/k8s-patterns.md](references/k8s-patterns.md) for Deployment, Service,
and StatefulSet patterns specific to PostgreSQL on Kubernetes.
