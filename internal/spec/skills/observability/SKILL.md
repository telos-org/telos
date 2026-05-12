---
name: observability
description: |
  Observability stack operations — Prometheus, Grafana, Loki, Alertmanager,
  OpenTelemetry Collector. Use when managing monitoring, dashboards, logs,
  and alerting infrastructure. Triggers on: observability category, prometheus/loki/http protocols.
metadata:
  category: observability
  protocols: prometheus,loki,http
  author: telos
allowed-tools: Bash(curl:*) Bash(kubectl:*)
---

# Observability Stack Operations

You are operating an observability stack: Prometheus (metrics), Grafana
(dashboards), Loki (logs), Alertmanager (alerts), and optionally
OpenTelemetry Collector (telemetry pipeline).

## Health Checks

```bash
# Prometheus
curl -sf http://<host>:9090/-/healthy && echo "OK"
curl -sf http://<host>:9090/-/ready && echo "Ready"

# Grafana
curl -sf http://<host>:3000/api/health | jq .

# Loki
curl -sf http://<host>:3100/ready && echo "Ready"

# Alertmanager
curl -sf http://<host>:9093/-/healthy && echo "OK"

# OTel Collector (if exposed)
curl -sf http://<host>:13133/health && echo "OK"
```

## Prometheus

### Configuration

```yaml
# prometheus.yml
global:
  scrape_interval: 15s
  evaluation_interval: 15s

scrape_configs:
  - job_name: 'kubernetes-pods'
    kubernetes_sd_configs:
      - role: pod
    relabel_configs:
      - source_labels: [__meta_kubernetes_pod_annotation_prometheus_io_scrape]
        action: keep
        regex: true
```

### Querying

```bash
# Instant query
curl -s 'http://<host>:9090/api/v1/query?query=up' | jq '.data.result'

# Range query (last 5m)
curl -s 'http://<host>:9090/api/v1/query_range?query=rate(http_requests_total[5m])&start=2024-01-01T00:00:00Z&end=2024-01-01T01:00:00Z&step=60s'

# Check targets
curl -s 'http://<host>:9090/api/v1/targets' | jq '.data.activeTargets[] | {job: .labels.job, health: .health}'

# Retention
curl -s 'http://<host>:9090/api/v1/status/flags' | jq '.data["storage.tsdb.retention.time"]'
```

## Grafana

### Provisioning

Grafana auto-loads datasources and dashboards from provisioning directories:

```
grafana/provisioning/
├── datasources/
│   └── datasources.yml
└── dashboards/
    └── dashboards.yml
```

### Datasource Config

```yaml
apiVersion: 1
datasources:
  - name: Prometheus
    type: prometheus
    url: http://prometheus:9090
    isDefault: true
  - name: Loki
    type: loki
    url: http://loki:3100
```

## Loki

### Pushing Logs

```bash
curl -X POST http://<host>:3100/loki/api/v1/push \
  -H 'Content-Type: application/json' \
  -d '{"streams":[{"stream":{"app":"test"},"values":[["'$(date +%s)000000000'","test log"]]}]}'
```

### Querying Logs

```bash
curl -s 'http://<host>:3100/loki/api/v1/query_range?query={app="test"}&limit=10' | jq .
```

## Verification Test Targets

```python
# test_observability.py
import subprocess, json

def test_prometheus_healthy():
    r = subprocess.run(["curl", "-sf", "http://localhost:9090/-/healthy"],
                       capture_output=True, timeout=5)
    assert r.returncode == 0

def test_grafana_healthy():
    r = subprocess.run(["curl", "-sf", "http://localhost:3000/api/health"],
                       capture_output=True, text=True, timeout=5)
    assert r.returncode == 0
    assert json.loads(r.stdout)["database"] == "ok"

def test_prometheus_has_targets():
    r = subprocess.run(["curl", "-sf", "http://localhost:9090/api/v1/targets"],
                       capture_output=True, text=True, timeout=5)
    data = json.loads(r.stdout)
    assert len(data["data"]["activeTargets"]) > 0
```
