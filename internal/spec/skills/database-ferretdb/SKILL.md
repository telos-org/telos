---
name: database-ferretdb
description: |
  FerretDB operations — MongoDB wire-compatible proxy backed by PostgreSQL.
  Use when managing FerretDB deployments: proxy config, Postgres backend,
  MongoDB compatibility, diagnostics, benchmarking.
  Triggers on: ferretdb, mongodb wire protocol, database category.
metadata:
  category: database
  protocols: mongodb
  author: telos
allowed-tools: Bash(mongosh:*) Bash(psql:*) Bash(curl:*)
---

# Database Operations (FerretDB — MongoDB Wire Protocol)

You are operating FerretDB, a MongoDB wire-compatible proxy that uses PostgreSQL
as its storage backend. Clients connect via the MongoDB wire protocol (port 27017).
FerretDB translates operations to SQL and stores data in Postgres.

## Architecture

```
Client (mongosh) → FerretDB (port 27017) → PostgreSQL (port 5432)
```

You must deploy BOTH FerretDB and PostgreSQL. FerretDB needs the Postgres
connection string via `FERRETDB_POSTGRESQL_URL`.

## Deploying FerretDB

### Environment variables
```
FERRETDB_POSTGRESQL_URL=postgres://user:pass@postgres:5432/ferretdb
FERRETDB_LISTEN_ADDR=:27017
```

### Verify FerretDB is connected to Postgres
```bash
curl -s http://localhost:8088/debug/serverStatus | jq .
```

### Verify MongoDB wire compatibility
```bash
mongosh --port 27017 --eval "db.runCommand({ping: 1})"
mongosh --port 27017 --eval "db.version()"
```

## Diagnostics

### FerretDB health
```bash
curl -s http://localhost:8088/debug/serverStatus | jq '{version, uptimeSeconds}'
```

### Postgres backend health
```bash
psql $FERRETDB_POSTGRESQL_URL -c "SELECT pg_database_size('ferretdb');"
psql $FERRETDB_POSTGRESQL_URL -c "SELECT count(*) FROM information_schema.tables WHERE table_schema NOT IN ('pg_catalog', 'information_schema');"
```

### MongoDB operations via mongosh
```bash
mongosh --port 27017 --eval "db.serverStatus().connections" --quiet
mongosh --port 27017 --eval "db.stats()" --quiet
```

## Benchmarking

### Write throughput
```bash
mongosh --port 27017 --eval "
for (var i = 0; i < 10000; i++) {
  db.bench.insertOne({ts: new Date(), val: Math.random(), idx: i});
}
print('Inserted 10000 docs');
db.bench.drop();
" --quiet
```

## Common Failure Patterns

1. **FerretDB can't connect to Postgres**: Check FERRETDB_POSTGRESQL_URL, Postgres is running, database exists
2. **Unsupported MongoDB operation**: FerretDB doesn't support all MongoDB features — check compatibility docs. Aggregation pipeline has gaps.
3. **Slow queries**: Performance depends on Postgres — check pg_stat_statements, vacuum, indexes
4. **Auth failures**: FerretDB delegates auth to Postgres. Check pg_hba.conf and Postgres roles.
5. **Connection refused on 27017**: FerretDB not running or FERRETDB_LISTEN_ADDR misconfigured
