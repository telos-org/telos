---
name: policy-encryption
description: |
  Encryption overlay. Use when the spec requires protected credentials, encrypted
  transport, or practical encryption at rest. Prefer realistic protection for the
  current substrate over fake compliance theater. Triggers on: encryption, TLS,
  protected secrets, secure transport, encryption at rest.
metadata:
  category: policy
  author: telos
allowed-tools: Bash(kubectl:*) Bash(openssl:*) Bash(psql:*) Bash(redis-cli:*) Bash(curl:*)
---

# Policy Overlay: Encryption

Goal: data and credentials are protected in the strongest practical way the
environment supports.

## Default Approach

Split the policy into three checks:

- credentials are not hardcoded
- transport is protected or explicitly constrained
- stored data is encrypted in a real, defensible way

## Cloud Runtime Reality Check

Full node-level disk encryption is not normally controlled from inside a
cloud workload namespace. Do not waste time on fake LUKS stories inside an
unprivileged pod.

Prefer:

- native TLS for client/server traffic
- strong password auth (`scram-sha-256`, ACLs, tokens)
- application-level or product-level encryption for stored secrets/data

## Verification Pattern

Always prove the concrete control:

- inspect the deployed config, not just the workspace
- test a secure client path
- prove secrets come from K8s Secret/env/volume, not hardcoded literals

Examples:

- PostgreSQL: `SHOW password_encryption;`, inspect `pg_hba.conf`, verify secret-backed env
- Valkey: require auth or ACLs, verify TLS if enabled
- OpenBao: verify auth token flow and storage/audit configuration without exposing secret values

## Good Outputs

Produce:

- manifest/config changes that enable the real protection
- one verification test for auth/transport
- one verification check that secrets are not hardcoded

## Anti-Patterns

- claiming encryption at rest from a PVC alone
- checking only that a secret exists, not that the workload actually uses it
- printing private keys, passwords, or secret values into logs
- enabling a TLS setting without testing a real connection path
