---
name: k8s-fault-network
description: |
  Network and service routing fault injection — selector mismatches, port
  mapping errors, NetworkPolicy denials, DNS failures. Pods run but cannot
  communicate. Medium difficulty: requires understanding K8s service discovery
  and network policy evaluation.
metadata:
  category: fault-injection
  role: environment-generator
  difficulty: medium
  source: cloud-opsbench
  author: telos
allowed-tools: Bash(kubectl:*)
---

# Network & Service Routing Fault Injection

Faults in this category break the network plane. Pods are Running and
containers are healthy, but services can't route traffic. The symptoms are
connection timeouts, connection refused, and DNS resolution failures — all
requiring the agent to trace from application errors back through the K8s
networking stack.

## Operators

### ServiceSelectorMismatch

Change the Service selector so it no longer matches pod labels. The Service
exists but has empty endpoints — traffic goes nowhere.

```bash
fault_selector_mismatch() {
  local svc=$1 ns=$2
  local wrong_value=${3:-"${svc}-typo"}

  kubectl patch svc "$svc" -n "$ns" --type=json -p "[
    {\"op\":\"replace\",\"path\":\"/spec/selector/app\",\"value\":\"$wrong_value\"}
  ]"
}

verify_selector_mismatch() {
  local svc=$1 ns=$2
  local endpoints=$(kubectl get endpoints "$svc" -n "$ns" \
    -o jsonpath='{.subsets[*].addresses[*].ip}' 2>/dev/null)
  [ -z "$endpoints" ] && echo "OK: endpoints empty" || echo "FAIL: endpoints still populated"
}
```

### ServicePortMappingMismatch

Service port or targetPort doesn't match what the container actually listens on.
Service has endpoints (selector matches) but traffic hits the wrong port.

```bash
fault_port_mismatch() {
  local svc=$1 ns=$2
  local wrong_target_port=${3:-"9999"}

  kubectl patch svc "$svc" -n "$ns" --type=json -p "[
    {\"op\":\"replace\",\"path\":\"/spec/ports/0/targetPort\",\"value\":$wrong_target_port}
  ]"
}

# Variant: change the service port (what clients connect to)
fault_service_port_change() {
  local svc=$1 ns=$2
  local wrong_port=${3:-"9999"}

  kubectl patch svc "$svc" -n "$ns" --type=json -p "[
    {\"op\":\"replace\",\"path\":\"/spec/ports/0/port\",\"value\":$wrong_port}
  ]"
}

verify_port_mismatch() {
  local svc=$1 ns=$2
  local target_port=$(kubectl get svc "$svc" -n "$ns" \
    -o jsonpath='{.spec.ports[0].targetPort}')
  echo "Service targetPort: $target_port"
  # Agent must compare this against the container's actual listening port
}
```

### ServiceProtocolMismatch

Change the service port protocol (TCP vs UDP). If the application uses TCP
but the Service specifies UDP (or vice versa), connections fail silently.

```bash
fault_protocol_mismatch() {
  local svc=$1 ns=$2
  local wrong_protocol=${3:-"UDP"}

  kubectl patch svc "$svc" -n "$ns" --type=json -p "[
    {\"op\":\"replace\",\"path\":\"/spec/ports/0/protocol\",\"value\":\"$wrong_protocol\"}
  ]"
}
```

### ServiceEnvVarAddressMismatch

Applications that discover services via environment variables (not DNS) break
when the env var points to the wrong service name or IP.

```bash
fault_service_env_address() {
  local deploy=$1 ns=$2
  local env_name=$3
  local wrong_address=$4

  kubectl set env deployment/"$deploy" -n "$ns" "$env_name=$wrong_address"
}

# Common patterns:
# fault_service_env_address "app" "$NS" "POSTGRES_HOST" "postgres-old.default.svc"
# fault_service_env_address "app" "$NS" "REDIS_URL" "redis://wrong-host:6379"
# fault_service_env_address "app" "$NS" "KAFKA_BROKERS" "kafka-0.wrong-svc:9092"
```

### NetworkPolicy Deny

Apply a NetworkPolicy that blocks traffic between services. All pods run
fine individually but can't communicate. This is particularly hard to
diagnose because there's no explicit error — connections just time out.

```bash
fault_network_policy_deny_all() {
  local ns=$1

  # Deny all ingress — nothing can reach any pod in the namespace
  kubectl apply -n "$ns" -f - <<EOF
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: deny-all-ingress
  namespace: $ns
spec:
  podSelector: {}
  policyTypes:
  - Ingress
EOF
}

fault_network_policy_deny_egress() {
  local ns=$1

  # Deny all egress — pods can't reach anything (DNS, other services, internet)
  kubectl apply -n "$ns" -f - <<EOF
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: deny-all-egress
  namespace: $ns
spec:
  podSelector: {}
  policyTypes:
  - Egress
EOF
}

# Targeted: block traffic between specific services
fault_network_policy_block_pair() {
  local ns=$1
  local from_label=$2  # e.g., "app=frontend"
  local to_label=$3    # e.g., "app=postgres"

  local to_key=$(echo "$to_label" | cut -d= -f1)
  local to_value=$(echo "$to_label" | cut -d= -f2)
  local from_key=$(echo "$from_label" | cut -d= -f1)
  local from_value=$(echo "$from_label" | cut -d= -f2)

  kubectl apply -n "$ns" -f - <<EOF
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: block-${from_value}-to-${to_value}
  namespace: $ns
spec:
  podSelector:
    matchLabels:
      $to_key: $to_value
  policyTypes:
  - Ingress
  ingress:
  - from:
    - podSelector:
        matchLabels:
          $from_key: not-$from_value
EOF
}

verify_network_policy() {
  local ns=$1
  kubectl get networkpolicy -n "$ns" --no-headers | wc -l | xargs -I{} \
    echo "NetworkPolicies in namespace: {}"
}
```

### DNS Disruption

Break DNS resolution by modifying the CoreDNS configmap or adding
a NetworkPolicy that blocks DNS traffic (port 53).

```bash
fault_block_dns() {
  local ns=$1

  # Block egress to kube-dns (UDP 53) — pods can't resolve service names
  kubectl apply -n "$ns" -f - <<EOF
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: block-dns
  namespace: $ns
spec:
  podSelector: {}
  policyTypes:
  - Egress
  egress:
  - to: []
    ports:
    - port: 53
      protocol: TCP
    - port: 53
      protocol: UDP
EOF
  # This blocks DNS but allows all other egress
  # Actually need to invert — allow everything EXCEPT DNS:
  kubectl delete networkpolicy block-dns -n "$ns" --ignore-not-found
  kubectl apply -n "$ns" -f - <<EOF
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: block-dns
  namespace: $ns
spec:
  podSelector: {}
  policyTypes:
  - Egress
  egress:
  - ports:
    - port: 443
      protocol: TCP
    - port: 80
      protocol: TCP
EOF
  # Only allow 80/443, implicitly denying DNS (53)
}
```

## Composition Patterns

```bash
# Selector mismatch + port mismatch — two independent routing failures
fault_selector_mismatch "postgres" "$NS"
fault_port_mismatch "redis" "$NS" "7777"

# NetworkPolicy + env var — agent fixes the env var but traffic still blocked
fault_service_env_address "app" "$NS" "DATABASE_HOST" "postgres-wrong"
fault_network_policy_deny_all "$NS"

# DNS block + service port change — can't resolve AND wrong port
fault_block_dns "$NS"
fault_service_port_change "postgres" "$NS" "5433"
```

## Symptom Characteristics

Medium difficulty:
- Connection timeout / refused errors in application logs
- Pods are Running and Ready (health checks may use localhost, not network)
- `kubectl get endpoints` reveals empty or wrong endpoints
- NetworkPolicy faults are hardest: no explicit error, pure timeout
- Agent must trace: app error → service → endpoints → selector/labels → fix
- DNS faults are subtle: `getaddrinfo` failures, `NXDOMAIN` in logs
