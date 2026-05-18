---
name: queue-kafka
description: |
  Apache Kafka queue/streaming operations. Use when managing Kafka protocol
  services — topic management, consumer groups, benchmarking, replication.
  Triggers on: kafka, queue category.
metadata:
  category: queue
  protocols: kafka
  author: telos
allowed-tools: Bash(kafka-topics.sh:*) Bash(kafka-console-producer.sh:*) Bash(kafka-console-consumer.sh:*) Bash(kafka-consumer-groups.sh:*) Bash(kafka-producer-perf-test.sh:*) Bash(kafka-consumer-perf-test.sh:*)
---

# Queue Operations (Kafka Protocol)

You are operating an Apache Kafka cluster. Diagnostics use the Kafka CLI scripts.
Benchmarking uses the built-in perf test tools.

## Diagnostics

### Cluster health
```bash
kafka-topics.sh --bootstrap-server $BOOTSTRAP:9092 --list
kafka-topics.sh --bootstrap-server $BOOTSTRAP:9092 --describe --topic __consumer_offsets
```

### Broker info
```bash
kafka-metadata.sh --snapshot /var/kafka-logs/__cluster_metadata-0/00000000000000000000.log --broker-id 0
```

### Consumer group lag
```bash
kafka-consumer-groups.sh --bootstrap-server $BOOTSTRAP:9092 --list
kafka-consumer-groups.sh --bootstrap-server $BOOTSTRAP:9092 --describe --all-groups
```

### Under-replicated partitions
```bash
kafka-topics.sh --bootstrap-server $BOOTSTRAP:9092 --describe --under-replicated-partitions
```

## Benchmarking

### Producer throughput
```bash
kafka-producer-perf-test.sh \
  --topic perf-test \
  --num-records 100000 \
  --record-size 1024 \
  --throughput -1 \
  --producer-props bootstrap.servers=$BOOTSTRAP:9092
```

### Consumer throughput
```bash
kafka-consumer-perf-test.sh \
  --bootstrap-server $BOOTSTRAP:9092 \
  --topic perf-test \
  --messages 100000
```

## Topic Management

### Create topic with replication
```bash
kafka-topics.sh --bootstrap-server $BOOTSTRAP:9092 \
  --create --topic my-topic \
  --partitions 6 --replication-factor 3
```

## TLS Configuration (KRaft mode, Kafka 3.5+)

Kafka's PEM keystore parser requires **PKCS#8** private keys. PKCS#1 keys
(`BEGIN RSA PRIVATE KEY`) will fail with `No matching PRIVATE KEY entries in PEM file`.

```bash
# Convert PKCS#1 → PKCS#8
openssl pkcs8 -topk8 -inform PEM -outform PEM -nocrypt -in tls.key -out tls-pkcs8.key

# Verify key format (must show BEGIN PRIVATE KEY, not BEGIN RSA PRIVATE KEY)
head -1 tls-pkcs8.key

# Verify cert and key match
openssl x509 -noout -modulus -in tls.crt | md5sum
openssl pkey -noout -modulus -in tls-pkcs8.key | md5sum
```

K8s secret for TLS:
```bash
kubectl create secret generic kafka-tls \
  --from-file=tls.crt=tls.crt \
  --from-file=tls.key=tls-pkcs8.key \
  --from-file=ca.crt=ca.crt \
  -n $NAMESPACE
```

Server properties for SSL + mTLS:
```properties
# Listeners — controller on PLAINTEXT, broker on SSL
listeners=SSL://0.0.0.0:9092,CONTROLLER://0.0.0.0:9093
inter.broker.listener.name=SSL
controller.listener.names=CONTROLLER

# PEM-based TLS (no JKS/PKCS12 keystores needed)
ssl.keystore.type=PEM
ssl.keystore.certificate.chain=/path/to/tls.crt
ssl.keystore.key=/path/to/tls-pkcs8.key
ssl.truststore.type=PEM
ssl.truststore.certificates=/path/to/ca.crt

# mTLS
ssl.client.auth=required
```

Certificate SAN must include all broker DNS names:
```
kafka-0.kafka-headless.<namespace>.svc.cluster.local
kafka-1.kafka-headless.<namespace>.svc.cluster.local
kafka-2.kafka-headless.<namespace>.svc.cluster.local
```

## Encryption at Rest

The default cloud k3s StorageClass should not be treated as application-level
encryption. Options:

1. **Application-level encryption**: Use Kafka interceptors
   or produce pre-encrypted messages. The `kafka-encryption-key` secret stores the key.
2. **dm-crypt/LUKS**: Requires privileged init container. Not viable in most k8s setups.
3. **gocryptfs/encfs**: FUSE-based, requires `--privileged` and careful init ordering.
   Race conditions between mount sidecar and Kafka startup are common — avoid unless necessary.

For cloud environments, application-level encryption is the practical path.

## Access Control (mTLS / SASL)

When `access_control: mtls` is required, configure:
```properties
ssl.client.auth=required
authorizer.class.name=org.apache.kafka.metadata.authorizer.StandardAuthorizer
allow.everyone.if.no.acl.found=false
```

Client TLS secret:
```bash
kubectl create secret generic kafka-client-tls \
  --from-file=client.crt --from-file=client.key --from-file=ca.crt \
  -n $NAMESPACE
```

## StatefulSet on Kubernetes

Key gotchas:
- `readOnlyRootFilesystem: true` breaks Kafka's GC logging — mount `emptyDir` at `/opt/kafka/logs`
- KRaft `controller.quorum.voters` must list all broker IDs and their CONTROLLER ports
- JMX exporter images: use `bitnami/jmx-exporter:1.0.1` (not `0.20.0` which doesn't exist)
- For 3-broker clusters: `default.replication.factor=3`, `min.insync.replicas=2`

## Common Failure Patterns

1. **SSL listener won't start**: Check key format (PKCS#8), cert/key modulus match, SAN entries
2. **Leader election stall**: Check controller quorum, KRaft voter config, ensure CONTROLLER listener is PLAINTEXT
3. **Consumer lag growing**: Check consumer group throughput vs produce rate, partition assignment
4. **ISR shrinking**: Check broker disk I/O, network between brokers, replica.lag.time.max.ms
5. **Disk full**: Check log retention (log.retention.hours, log.retention.bytes), topic compaction
6. **Connection refused**: Check listeners config, advertised.listeners, security.protocol
7. **CrashLoopBackOff**: Check image tags exist, readOnlyRootFilesystem conflicts, init container ordering
