---
name: policy-audit
description: |
  Auditable-system overlay. Use when the spec requires auditability, traceable
  admin actions, or proof that sensitive operations are recorded. Prefer native
  audit facilities over ad hoc logs. Triggers on: audit, audit log, traceability,
  compliance evidence, recorded access.
metadata:
  category: policy
  author: telos
allowed-tools: Bash(kubectl:*) Bash(psql:*) Bash(bao:*) Bash(curl:*) Bash(grep:*)
---

# Policy Overlay: Audit

Goal: a sensitive action must leave a durable, queryable audit record.

## Default Approach

Prefer the product's native audit facility:

- PostgreSQL: `pgaudit`, audit schema, or explicit immutable audit tables
- OpenBao: audit device via `bao audit enable ...`
- HTTP services: structured application audit log with actor, action, target, outcome

Do not claim "audit" from generic pod logs alone unless the spec explicitly allows that.

## Required Properties

- The audited action is real, not synthetic
- The record includes who acted, what changed, and when
- The record survives the immediate request path
- The record can be queried after the action completes

## Verification Pattern

Always verify audit with a real roundtrip:

1. Perform a sensitive operation
2. Capture the timestamp or request identifier
3. Query the audit sink
4. Assert the matching record exists

Examples of sensitive operations:

- create or rotate a secret
- create or drop a table
- write or delete application data
- change an auth policy or role

## Good Outputs

Produce:

- the audit configuration in manifests or config files
- one executable test that performs an audited action and proves the record exists
- brief operator notes on where audit records live

## Anti-Patterns

- treating stdout noise as a complete audit trail
- writing an audit test that only checks configuration, not emitted records
- logging sensitive payloads or raw secret values
- making audit logs writable by the application principal that is being audited
