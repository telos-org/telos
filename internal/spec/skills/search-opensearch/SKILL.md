---
name: search-opensearch
description: |
  OpenSearch/Elasticsearch search engine operations. Use when managing search
  services via REST API — cluster health, index management, benchmarking.
  Triggers on: opensearch, elasticsearch, search category.
metadata:
  category: search
  protocols: opensearch, http
  author: telos
allowed-tools: Bash(curl:*) Bash(opensearch-benchmark:*)
---

# Search Operations (OpenSearch REST API)

You are operating an OpenSearch cluster (Elasticsearch-compatible).
The interface is REST/HTTP. Diagnostics use `curl`, benchmarking uses
`opensearch-benchmark`.

## Diagnostics

### Cluster health
```bash
curl -s http://localhost:9200/_cluster/health?pretty
curl -s http://localhost:9200/_cat/nodes?v
curl -s http://localhost:9200/_cat/indices?v&s=index
```

### Shard allocation
```bash
curl -s http://localhost:9200/_cat/shards?v&s=index
curl -s http://localhost:9200/_cluster/allocation/explain?pretty
```

### Pending tasks and thread pools
```bash
curl -s http://localhost:9200/_cat/pending_tasks?v
curl -s http://localhost:9200/_cat/thread_pool?v&h=node_name,name,active,rejected,completed
```

### Index stats
```bash
curl -s http://localhost:9200/_stats?pretty
curl -s http://localhost:9200/<index>/_stats?pretty
```

## Benchmarking

### Index throughput
```bash
opensearch-benchmark execute-test \
  --target-hosts=localhost:9200 \
  --workload=geonames \
  --test-mode
```

## Index Management

### Create index with replicas
```bash
curl -X PUT http://localhost:9200/my-index -H 'Content-Type: application/json' -d '{
  "settings": {
    "number_of_shards": 3,
    "number_of_replicas": 1
  }
}'
```

## Common Failure Patterns

1. **Red cluster**: Unassigned primary shards — check disk watermarks, shard allocation filters
2. **Yellow cluster**: Missing replicas — check node count vs replica settings
3. **High JVM heap**: Check fielddata, request cache, bulk queue; tune circuit breakers
4. **Slow queries**: Check slow log, use `_explain` API, profile queries
5. **Split brain**: Check `minimum_master_nodes` (legacy) or voting config exclusions
